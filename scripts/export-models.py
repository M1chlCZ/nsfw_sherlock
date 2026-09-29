#!/usr/bin/env python3
"""Export the nsfw_sherlock classifier models to ONNX for the Go engine.

Usage: scripts/export-models.py --out assets/models [--only id]
"""

from __future__ import annotations

import argparse
import hashlib
import sys
from dataclasses import dataclass
from pathlib import Path

PINNED_VERSIONS = {
    "torch": "2.14.0",
    "transformers": "5.17.0",
    "timm": "1.0.30",
    "onnx": "1.23.0",
    "onnxruntime": "1.30.0",
}

MAX_ABS_DIFF = 1e-3


@dataclass(frozen=True)
class ExportSpec:
    id: str
    repo: str
    revision: str
    filename: str
    width: int
    height: int
    note: str


EXPORTS = (
    ExportSpec(
        id="freepik-eva02-448",
        repo="Freepik/nsfw_image_detector",
        revision="15b85477e4fd2000db76ae9aae0f89a72f95e2e3",
        filename="freepik-eva02-448.onnx",
        width=448,
        height=448,
        note="EVA02-base-448, 4 labels [neutral, low, medium, high]",
    ),
    ExportSpec(
        id="nsfw-vit5-224",
        repo="giacomoarienti/nsfw-classifier",
        revision="29f43cab33874e62db8a1973bd0f84b0c69ff057",
        filename="nsfw-vit5-224.onnx",
        width=224,
        height=224,
        note="ViT-base-224, 5 labels [drawings, hentai, neutral, porn, sexy]",
    ),
)


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        prog="export-models.py",
        description="Export nsfw_sherlock classifier models to ONNX (opset 17, dynamic batch).",
        epilog=(
            "Pinned versions: "
            + ", ".join(f"{k}=={v}" for k, v in PINNED_VERSIONS.items())
            + ". Pending: the models-v1 release the manifest points at does not "
            "exist yet, so scripts/fetch-models.sh 404s for freepik-eva02-448.onnx "
            "and nsfw-vit5-224.onnx until this script has exported them locally."
        ),
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    parser.add_argument(
        "--out",
        type=Path,
        help="output directory for the .onnx files and their .sha256 sidecars",
    )
    parser.add_argument(
        "--only",
        action="append",
        default=None,
        metavar="ID",
        help="export only the given model id (repeatable); see --list",
    )
    parser.add_argument(
        "--list",
        action="store_true",
        help="list the available model ids and exit",
    )
    return parser.parse_args(argv)


def deterministic_image(width: int = 512, height: int = 384):
    import numpy as np
    from PIL import Image

    x = np.arange(width, dtype=np.int64)[None, :]
    y = np.arange(height, dtype=np.int64)[:, None]
    r = (x * 255 // max(width - 1, 1)).astype(np.uint8)
    g = (y * 255 // max(height - 1, 1)).astype(np.uint8)
    b = (((x // 16) + (y // 16)) % 2 * 255).astype(np.uint8)
    channels = np.broadcast_arrays(r, g, b)
    arr = np.stack(channels, axis=-1).astype(np.uint8)
    return Image.fromarray(arr, "RGB")


def load_model(repo: str, revision: str):
    import torch
    from transformers import AutoModelForImageClassification

    kwargs = {"dtype": torch.float32, "use_safetensors": True, "revision": revision}
    try:
        model = AutoModelForImageClassification.from_pretrained(repo, **kwargs)
    except TypeError:
        kwargs["torch_dtype"] = kwargs.pop("dtype")
        model = AutoModelForImageClassification.from_pretrained(repo, **kwargs)
    model.eval()
    return model


def load_timm_fallback(repo: str, revision: str):
    import timm
    import torch
    from huggingface_hub import hf_hub_download
    from safetensors.torch import load_file
    from transformers import AutoConfig

    config = AutoConfig.from_pretrained(repo, revision=revision)
    architecture = config.architecture
    num_classes = int(config.num_classes)
    global_pool = getattr(config, "global_pool", None) or "avg"

    model = timm.create_model(
        architecture,
        pretrained=False,
        num_classes=num_classes,
        global_pool=global_pool,
    )

    weights_path = hf_hub_download(repo_id=repo, filename="model.safetensors", revision=revision)
    state = load_file(weights_path)

    candidates = [
        state,
        {key.removeprefix("model."): value for key, value in state.items()},
        {f"model.{key}": value for key, value in state.items()},
    ]
    for candidate in candidates:
        missing, unexpected = model.load_state_dict(candidate, strict=False)
        if not missing and not unexpected:
            model.eval()
            return model
    raise RuntimeError(
        f"{repo}: timm fallback could not map model.safetensors onto "
        f"timm.create_model({architecture!r}): missing/extra keys remain"
    )


_PROCESSOR_FALLBACKS = {
    "ViTFeatureExtractor": "ViTImageProcessor",
    "ViTImageProcessor": "ViTImageProcessor",
    "TimmWrapperImageProcessor": "TimmWrapperImageProcessor",
}


def load_processor(repo: str, revision: str):
    import json as _json

    import transformers
    from transformers import AutoImageProcessor

    try:
        return AutoImageProcessor.from_pretrained(repo, revision=revision)
    except Exception as auto_error:  # noqa: BLE001 - concrete fallback below
        from huggingface_hub import hf_hub_download

        try:
            config_path = hf_hub_download(
                repo_id=repo, filename="preprocessor_config.json", revision=revision
            )
            with open(config_path, encoding="utf-8") as fh:
                declared = _json.load(fh).get("image_processor_type")
        except Exception:  # noqa: BLE001 - keep the original error
            declared = None
        class_name = _PROCESSOR_FALLBACKS.get(declared)
        if class_name is None:
            raise auto_error
        processor_class = getattr(transformers, class_name, None)
        if processor_class is None:
            raise auto_error
        return processor_class.from_pretrained(repo, revision=revision)


def load_model_and_processor(spec: ExportSpec):
    processor = load_processor(spec.repo, spec.revision)

    try:
        return load_model(spec.repo, spec.revision), processor
    except Exception as first_error:  # noqa: BLE001 - reported below
        print(
            f"{spec.id}: AutoModelForImageClassification failed ({first_error}); "
            "falling back to timm.create_model + load_state_dict",
            file=sys.stderr,
        )
        return load_timm_fallback(spec.repo, spec.revision), processor


def preprocess(processor, image):
    import torch

    batch = processor(images=image, return_tensors="pt")
    if "pixel_values" not in batch:
        raise RuntimeError(f"processor returned keys {list(batch)} without pixel_values")
    return batch["pixel_values"].to(torch.float32)


def export_one(spec: ExportSpec, out_dir: Path) -> Path:
    import numpy as np
    import onnx
    import onnxruntime as ort
    import torch

    model, processor = load_model_and_processor(spec)
    image = deterministic_image()
    pixel_values = preprocess(processor, image)

    with torch.no_grad():
        reference = model(pixel_values)
    logits = getattr(reference, "logits", reference)
    reference_np = logits.detach().cpu().numpy()

    out_dir.mkdir(parents=True, exist_ok=True)
    path = out_dir / spec.filename
    torch.onnx.export(
        model,
        (pixel_values,),
        str(path),
        input_names=["pixel_values"],
        output_names=["logits"],
        opset_version=17,
        dynamo=False,
        do_constant_folding=True,
        dynamic_axes={"pixel_values": {0: "batch"}, "logits": {0: "batch"}},
    )

    onnx_model = onnx.load(str(path), load_external_data=False)
    inputs = list(onnx_model.graph.input)
    outputs = list(onnx_model.graph.output)
    if len(inputs) != 1 or len(outputs) != 1:
        raise RuntimeError(
            f"{spec.id}: exported model has {len(inputs)} inputs and "
            f"{len(outputs)} outputs, want exactly one of each"
        )
    input_name = inputs[0].name
    output_name = outputs[0].name

    session = ort.InferenceSession(str(path), providers=["CPUExecutionProvider"])
    feed = {input_name: pixel_values.numpy()}
    got = session.run([output_name], feed)[0]

    if got.shape != reference_np.shape:
        raise RuntimeError(
            f"{spec.id}: onnxruntime output shape {got.shape} != torch {reference_np.shape}"
        )
    max_diff = float(np.max(np.abs(got - reference_np)))
    if not np.isfinite(max_diff) or max_diff >= MAX_ABS_DIFF:
        raise RuntimeError(
            f"{spec.id}: torch/onnx max abs diff {max_diff} exceeds {MAX_ABS_DIFF}"
        )

    batch_two = session.run([output_name], {input_name: np.concatenate([feed[input_name]] * 2, axis=0)})[0]
    if batch_two.shape[0] != 2:
        raise RuntimeError(f"{spec.id}: batch 2 export returned shape {batch_two.shape}")
    batch_diff = float(np.max(np.abs(batch_two[0] - got[0])))
    if not np.isfinite(batch_diff) or batch_diff > MAX_ABS_DIFF:
        raise RuntimeError(f"{spec.id}: batch 2 output differs from batch 1 by {batch_diff}")

    digest = hashlib.sha256(path.read_bytes()).hexdigest()
    sidecar = path.with_suffix(path.suffix + ".sha256")
    sidecar.write_text(f"{digest}  {path.name}\n", encoding="utf-8")

    print(
        f"{spec.id}: wrote {path.name} ({path.stat().st_size} bytes)\n"
        f"  input={input_name} shape={[d.dim_param or d.dim_value for d in inputs[0].type.tensor_type.shape.dim]} "
        f"output={output_name} shape={[d.dim_param or d.dim_value for d in outputs[0].type.tensor_type.shape.dim]}\n"
        f"  max abs diff torch/onnx={max_diff:.3e} (limit {MAX_ABS_DIFF:.0e}), "
        f"batch-2 diff={batch_diff:.3e}\n"
        f"  sha256={digest}"
    )
    return path


def main(argv: list[str] | None = None) -> int:
    args = parse_args(argv)

    if args.list:
        for spec in EXPORTS:
            print(f"{spec.id}\t{spec.repo}@{spec.revision}\t{spec.filename}\t{spec.note}")
        return 0

    if args.out is None:
        print("error: --out is required unless --list is used", file=sys.stderr)
        return 2

    try:
        import onnx
        import onnxruntime
        import timm
        import torch
        import transformers
    except ImportError as err:
        print(f"error: missing dependency: {err}", file=sys.stderr)
        print(
            "install the pinned environment: " + ", ".join(f"{k}=={v}" for k, v in PINNED_VERSIONS.items()),
            file=sys.stderr,
        )
        return 1

    versions = {
        "torch": torch.__version__,
        "transformers": transformers.__version__,
        "timm": timm.__version__,
        "onnx": onnx.__version__,
        "onnxruntime": onnxruntime.__version__,
    }
    for name, version in versions.items():
        print(f"{name}=={version}")

    selected = list(EXPORTS)
    if args.only:
        known = {spec.id for spec in EXPORTS}
        unknown = [want for want in args.only if want not in known]
        if unknown:
            print(f"error: unknown model id(s) {unknown}; known ids: {sorted(known)}", file=sys.stderr)
            return 2
        wanted = set(args.only)
        selected = [spec for spec in EXPORTS if spec.id in wanted]

    for spec in selected:
        try:
            export_one(spec, args.out)
        except Exception as err:  # noqa: BLE001 - top-level report
            print(f"error: {spec.id}: {err}", file=sys.stderr)
            return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())

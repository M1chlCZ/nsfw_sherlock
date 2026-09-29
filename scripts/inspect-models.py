#!/usr/bin/env python3
"""Inspect the ONNX models used by nsfw_sherlock and dump parity fixtures.

Usage: scripts/inspect-models.py --dir assets/models --fixtures testdata/parity
"""

from __future__ import annotations

import argparse
import ast
import hashlib
import json
import sys
from pathlib import Path

NUDENET_V34_CLASSES = [
    "FEMALE_GENITALIA_COVERED",
    "FACE_FEMALE",
    "BUTTOCKS_EXPOSED",
    "FEMALE_BREAST_EXPOSED",
    "FEMALE_GENITALIA_EXPOSED",
    "MALE_BREAST_EXPOSED",
    "ANUS_EXPOSED",
    "FEET_EXPOSED",
    "BELLY_COVERED",
    "FEET_COVERED",
    "ARMPITS_COVERED",
    "ARMPITS_EXPOSED",
    "FACE_MALE",
    "BELLY_EXPOSED",
    "MALE_GENITALIA_EXPOSED",
    "ANUS_COVERED",
    "FEMALE_BREAST_COVERED",
    "BUTTOCKS_COVERED",
]

HF_PROCESSORS = {
    "freepik-eva02-448.onnx": {
        "repo": "Freepik/nsfw_image_detector",
        "kind": "auto",
        "revision": "15b85477e4fd2000db76ae9aae0f89a72f95e2e3",
    },
    "nsfw-vit5-224.onnx": {
        "repo": "giacomoarienti/nsfw-classifier",
        "kind": "vit",
        "revision": "29f43cab33874e62db8a1973bd0f84b0c69ff057",
    },
}
TIMM_MODELS = {
    "caformer_s36_plus.onnx": "caformer_s36.sail_in22k_ft_in1k_384",
    "mobilenetv3_sce_dist.onnx": "mobilenetv3_large_100",
}
NUDENET_FILES = ("320n.onnx", "640m.onnx")

_PROCESSOR_FALLBACKS = {
    "ViTFeatureExtractor": "ViTImageProcessor",
    "ViTImageProcessor": "ViTImageProcessor",
    "TimmWrapperImageProcessor": "TimmWrapperImageProcessor",
}

NUDENET_CANDIDATE_THRESHOLD = 0.2
NUDENET_SCORE_THRESHOLD = 0.25
NUDENET_IOU_THRESHOLD = 0.45

PARITY_SCHEMA_VERSION = 2

PROBABILITY_TOLERANCE = 1e-3

DEFAULT_MEAN = [0.485, 0.456, 0.406]
DEFAULT_STD = [0.229, 0.224, 0.225]


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        prog="inspect-models.py",
        description="Inspect ONNX models and write parity fixtures.",
        epilog=(
            "Pending: the models-v1 release for freepik-eva02-448.onnx and "
            "nsfw-vit5-224.onnx does not exist yet, so export those locally with "
            "scripts/export-models.py --out assets/models before fetching with "
            "scripts/fetch-models.sh."
        ),
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    parser.add_argument("--dir", type=Path, default=Path("assets/models"), help="directory with *.onnx files")
    parser.add_argument("--fixtures", type=Path, default=Path("testdata/parity"), help="fixture output directory")
    parser.add_argument(
        "--manifest",
        type=Path,
        default=Path("models/manifest.json"),
        help="manifest used to resolve per-model preprocessing (optional)",
    )
    parser.add_argument(
        "--no-hf-config",
        action="store_true",
        help="skip Hugging Face revision lookups and processor config printing",
    )
    return parser.parse_args(argv)


def load_manifest(path: Path) -> dict:
    try:
        with open(path, encoding="utf-8") as fh:
            return json.load(fh)
    except (OSError, ValueError):
        return {}


def manifest_entry(manifest: dict, file_name: str) -> dict | None:
    for model in manifest.get("models", []):
        if model.get("file") == file_name:
            return model
    return None


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


def tensor_dims(value) -> list:
    dims = []
    for dim in value.type.tensor_type.shape.dim:
        if dim.HasField("dim_value"):
            dims.append(dim.dim_value)
        else:
            dims.append(dim.dim_param or "?")
    return dims


def elem_type_name(elem_type: int) -> str:
    import onnx

    try:
        return onnx.TensorProto.DataType.Name(elem_type).lower()
    except ValueError:
        return str(elem_type)


def parse_metadata_names(metadata: dict) -> list[str] | None:
    raw = metadata.get("names")
    if not raw:
        return None
    try:
        parsed = ast.literal_eval(raw)
    except (SyntaxError, ValueError):
        return None
    if not isinstance(parsed, dict):
        return None
    try:
        return [parsed[index] for index in range(len(parsed))]
    except KeyError:
        return None


def hf_revision(repo: str) -> str | None:
    try:
        from huggingface_hub import HfApi

        return HfApi().model_info(repo).sha
    except Exception:  # noqa: BLE001 - optional information
        return None


def pinned_revision(entry: dict | None) -> str | None:
    import re

    url = ((entry or {}).get("source") or {}).get("url") or ""
    match = re.search(r"/resolve/([0-9a-fA-F]{40})(?:/|$)", url)
    return match.group(1).lower() if match else None


def load_hf_processor(repo: str, kind: str, revision: str | None = None):
    import transformers

    kwargs = {"revision": revision} if revision else {}
    if kind == "vit":
        return transformers.ViTImageProcessor.from_pretrained(repo, **kwargs)

    from transformers import AutoImageProcessor

    try:
        return AutoImageProcessor.from_pretrained(repo, **kwargs)
    except Exception as auto_error:  # noqa: BLE001 - concrete fallback below
        from huggingface_hub import hf_hub_download

        try:
            config_path = hf_hub_download(
                repo_id=repo, filename="preprocessor_config.json", **kwargs
            )
            with open(config_path, encoding="utf-8") as fh:
                declared = json.load(fh).get("image_processor_type")
        except Exception:  # noqa: BLE001 - keep the original error
            declared = None
        class_name = _PROCESSOR_FALLBACKS.get(declared)
        if class_name is None:
            raise auto_error
        processor_class = getattr(transformers, class_name, None)
        if processor_class is None:
            raise auto_error
        return processor_class.from_pretrained(repo, **kwargs)


def hf_reference_tensor(image, repo: str, kind: str, want_revision: bool, revision: str | None = None):
    import torch

    processor = load_hf_processor(repo, kind, revision)
    batch = processor(images=image, return_tensors="pt")
    if "pixel_values" not in batch:
        raise RuntimeError(f"{repo}: processor returned {list(batch)} without pixel_values")
    tensor = batch["pixel_values"].to(torch.float32).detach().cpu().numpy()

    reference = {
        "kind": "hf_processor",
        "repo": repo,
        "processor": type(processor).__name__,
        "config": {
            key: value
            for key, value in processor.to_dict().items()
            if key
            in (
                "do_resize",
                "size",
                "do_center_crop",
                "crop_size",
                "resample",
                "image_mean",
                "image_std",
                "rescale_factor",
                "do_rescale",
                "do_normalize",
                "data_config",
            )
        },
    }
    if revision:
        reference["revision"] = revision
    elif want_revision:
        current = hf_revision(repo)
        if current:
            reference["revision"] = current
    return tensor, reference


def timm_reference_tensor(image, model_name: str):
    import timm

    model = timm.create_model(model_name, pretrained=False)
    config = timm.data.resolve_data_config({}, model=model)
    transform = timm.data.create_transform(**config)
    tensor = transform(image)
    tensor = tensor.detach().cpu().numpy()
    if tensor.ndim == 3:
        tensor = tensor[None]

    reference = {
        "kind": "timm_transform",
        "model": model_name,
        "timm": timm.__version__,
        "config": {
            "input_size": list(config["input_size"]),
            "interpolation": config["interpolation"],
            "crop_pct": config["crop_pct"],
            "crop_mode": config["crop_mode"],
            "mean": list(config["mean"]),
            "std": list(config["std"]),
        },
    }
    return tensor, reference


def nudenet_reference_tensor(image, size: int):
    import cv2
    import numpy as np

    mat = np.asarray(image.convert("RGB"), dtype=np.uint8)[:, :, ::-1]
    mat_c3 = cv2.cvtColor(mat, cv2.COLOR_RGBA2BGR)
    height, width = mat_c3.shape[:2]
    max_size = max(height, width)
    padded = cv2.copyMakeBorder(
        mat_c3, 0, max_size - height, 0, max_size - width, cv2.BORDER_CONSTANT
    )
    blob = cv2.dnn.blobFromImage(
        padded, 1 / 255.0, (size, size), (0, 0, 0), swapRB=True, crop=False
    )
    return np.ascontiguousarray(blob, dtype=np.float32)


def nms_cv2(boxes, scores, score_threshold: float, nms_threshold: float) -> list[int]:
    indices = [i for i in range(len(scores)) if scores[i] > score_threshold]
    indices.sort(key=lambda i: scores[i], reverse=True)
    kept: list[int] = []
    suppressed = [False] * len(scores)
    for position, i in enumerate(indices):
        if suppressed[i]:
            continue
        kept.append(i)
        ax, ay, aw, ah = boxes[i]
        ax2, ay2 = ax + aw, ay + ah
        for j in indices[position + 1 :]:
            if suppressed[j]:
                continue
            bx, by, bw, bh = boxes[j]
            inter_w = min(ax2, bx + bw) - max(ax, bx)
            inter_h = min(ay2, by + bh) - max(ay, by)
            inter = inter_w * inter_h if inter_w > 0 and inter_h > 0 else 0.0
            union = aw * ah + bw * bh - inter
            if union > 0 and inter / union > nms_threshold:
                suppressed[j] = True
    return kept


def nudenet_reference_detections(output, src_w: int, src_h: int, model_size: int) -> list[dict]:
    import numpy as np

    rows = np.squeeze(np.asarray(output[0])).T
    max_size = max(src_w, src_h)
    scale = max_size / model_size
    boxes: list[list[float]] = []
    scores: list[float] = []
    class_ids: list[int] = []
    for row in rows:
        classes_scores = row[4:]
        max_score = float(np.max(classes_scores))
        if max_score < NUDENET_CANDIDATE_THRESHOLD:
            continue
        class_id = int(np.argmax(classes_scores))
        x, y, w, h = (float(value) for value in row[:4])
        x -= w / 2
        y -= h / 2
        x *= scale
        y *= scale
        w *= scale
        h *= scale
        x = max(0.0, min(x, float(src_w)))
        y = max(0.0, min(y, float(src_h)))
        w = min(w, src_w - x)
        h = min(h, src_h - y)
        boxes.append([x, y, w, h])
        scores.append(max_score)
        class_ids.append(class_id)

    kept = nms_cv2(boxes, scores, NUDENET_SCORE_THRESHOLD, NUDENET_IOU_THRESHOLD)
    return [
        {
            "label": NUDENET_V34_CLASSES[class_ids[i]],
            "score": scores[i],
            "box": {"x": boxes[i][0], "y": boxes[i][1], "w": boxes[i][2], "h": boxes[i][3]},
        }
        for i in kept
    ]


def classify_outputs(array) -> dict:
    import numpy as np

    flat = array.reshape(-1).astype(np.float64)
    in_range = bool(np.all(flat >= 0.0) and np.all(flat <= 1.0))
    total = float(flat.sum())
    is_probability = in_range and abs(total - 1.0) <= PROBABILITY_TOLERANCE
    return {"is_probability": is_probability, "sum": total, "min": float(flat.min()), "max": float(flat.max())}


def activate(values, activation: str):
    import numpy as np

    flat = values.reshape(-1).astype(np.float64)
    if activation != "softmax":
        return flat
    shifted = np.exp(flat - flat.max())
    return shifted / shifted.sum()


def detector_fixture_image():
    from pathlib import Path

    from PIL import Image

    path = Path("testdata/pic.jpg")
    if not path.is_file():
        raise RuntimeError(
            f"detector fixtures require {path}; the synthetic image has no reference detections"
        )
    image = Image.open(path).convert("RGB")
    return image, {"generator": "file", "path": str(path), "width": image.width, "height": image.height}


def preprocess_record(tensor, width, height, mean, std, normalize, crop_pct, interpolation, image_info) -> dict:
    import numpy as np

    contiguous = np.ascontiguousarray(tensor, dtype=np.float32)
    return {
        "width": width,
        "height": height,
        "mean": list(mean),
        "std": list(std),
        "normalize": normalize,
        "crop_pct": crop_pct,
        "interpolation": interpolation,
        "tensor_shape": list(contiguous.shape),
        "tensor_sha256": hashlib.sha256(contiguous.tobytes()).hexdigest(),
        "tensor_sha256_note": "informational: the Go engine may round differently; compare tensor_stats",
        "tensor_stats": {
            "min": float(contiguous.min()),
            "max": float(contiguous.max()),
            "mean": float(contiguous.mean()),
        },
        "image": image_info,
    }


def print_processor_config(reference: dict, enabled: bool) -> None:
    if not enabled:
        return
    if reference.get("kind") == "hf_processor":
        print(f"  hf processor ({reference['repo']}, revision {reference.get('revision', '?')}):")
        for key, value in reference["config"].items():
            print(f"    {key} = {json.dumps(value)}")
    elif reference.get("kind") == "timm_transform":
        print(f"  timm transform ({reference['model']}, timm {reference['timm']}):")
        for key, value in reference["config"].items():
            print(f"    {key} = {json.dumps(value)}")
    elif reference.get("kind") == "nudenet":
        print(
            "  nudenet reference preprocessing: top-left scale, black right/bottom padding, "
            f"BGR, input_size={reference['input_size']}, candidate>={NUDENET_CANDIDATE_THRESHOLD}, "
            f"NMS {NUDENET_SCORE_THRESHOLD}/{NUDENET_IOU_THRESHOLD}"
        )


def inspect_file(path: Path, manifest: dict, fixtures_dir: Path, hf_config: bool) -> dict:
    import numpy as np
    import onnx
    import onnxruntime as ort

    model = onnx.load(str(path), load_external_data=False)
    metadata = {prop.key: prop.value for prop in model.metadata_props}

    print(f"\n=== {path.name} ({path.stat().st_size} bytes)")
    print(
        f"  ir_version={model.ir_version} "
        f"opset={[str(op.domain or 'ai.onnx') + ':' + str(op.version) for op in model.opset_import]}"
    )
    inputs = [
        (value.name, tensor_dims(value), elem_type_name(value.type.tensor_type.elem_type))
        for value in model.graph.input
    ]
    outputs = [
        (value.name, tensor_dims(value), elem_type_name(value.type.tensor_type.elem_type))
        for value in model.graph.output
    ]
    for name, dims, dtype in inputs:
        print(f"  input:  {name} shape={dims} dtype={dtype}")
    for name, dims, dtype in outputs:
        print(f"  output: {name} shape={dims} dtype={dtype}")
    if metadata:
        print("  metadata_props:")
        for key, value in metadata.items():
            print(f"    {key} = {value}")
    else:
        print("  metadata_props: (none)")

    entry = manifest_entry(manifest, path.name)
    kind = (entry or {}).get("kind", "classifier")
    role = (entry or {}).get("role", "")
    labels = (entry or {}).get("labels", [])
    spec = (entry or {}).get("input", {})
    image = deterministic_image()

    fixture = {
        "schema_version": PARITY_SCHEMA_VERSION,
        "model": path.name,
        "sha256": hashlib.sha256(path.read_bytes()).hexdigest(),
        "kind": kind,
        "role": role,
        "labels": labels,
        "input_names": [name for name, _, _ in inputs],
        "output_names": [name for name, _, _ in outputs],
        "activation": "none",
        "tolerance": {"score": 0.05} if kind != "detector" else {"score": 0.05, "box_px": 2},
        "outputs": {},
    }

    if kind == "detector" or path.name in NUDENET_FILES:
        import cv2

        size = int(spec.get("width") or 320)
        image, image_info = detector_fixture_image()
        tensor = nudenet_reference_tensor(image, size)
        reference = {
            "kind": "nudenet",
            "implementation": "nudenet 3.4.2 _read_image (cv2 blobFromImage) / _postprocess (numpy)",
            "opencv": cv2.__version__,
            "input_size": size,
            "channel_order": "BGR",
            "padding": "black, right/bottom, top-left anchored",
        }
        fixture["reference"] = reference
        fixture["detector"] = {
            "classes": NUDENET_V34_CLASSES,
            "score_threshold": NUDENET_SCORE_THRESHOLD,
            "iou_threshold": NUDENET_IOU_THRESHOLD,
        }
        fixture["preprocess"] = preprocess_record(
            tensor, size, size, [0, 0, 0], [0, 0, 0], False, 0, "bilinear", image_info
        )
        print_processor_config(reference, hf_config)

        session = ort.InferenceSession(str(path), providers=["CPUExecutionProvider"])
        feed = {session.get_inputs()[0].name: tensor}
        raw_outputs = session.run(None, feed)
        for (name, dims, _), raw in zip(outputs, raw_outputs):
            class_scores = raw[:, 4:, :] if raw.ndim == 3 else raw[4:, :]
            print(
                f"  {name}: shape={list(raw.shape)} -> raw detector tensor "
                f"(class scores {float(class_scores.min()):.6f}..{float(class_scores.max()):.6f})"
            )
            fixture["outputs"][name] = {
                "shape": list(raw.shape),
                "stats": {
                    "class_score_min": float(class_scores.min()),
                    "class_score_max": float(class_scores.max()),
                },
            }
        detections = nudenet_reference_detections(raw_outputs[0], image.width, image.height, size)
        fixture["detections"] = detections
        print(f"  reference detections ({len(detections)}):")
        for detection in detections:
            box = detection["box"]
            print(
                f"    {detection['label']} score={detection['score']:.6f} "
                f"box=({box['x']}, {box['y']}, {box['w']}, {box['h']})"
            )
        names = parse_metadata_names(metadata)
        rows = raw_outputs[0].shape[1]
        if names is not None:
            if rows != 4 + len(names):
                raise RuntimeError(f"{path.name}: output has {rows} rows, want 4 + {len(names)} classes")
            if names != NUDENET_V34_CLASSES:
                raise RuntimeError(f"{path.name}: metadata class order differs from NudeNet v3.4")
            print(f"  detector classes ({len(names)}): {names}")
    else:
        width = int(spec.get("width") or 224)
        height = int(spec.get("height") or width)
        mean = spec.get("mean", DEFAULT_MEAN)
        std = spec.get("std", DEFAULT_STD)
        crop_pct = float(spec.get("crop_pct") or 0.0)
        interpolation = spec.get("interpolation") or "bicubic"

        if path.name in HF_PROCESSORS:
            hf = HF_PROCESSORS[path.name]
            revision = pinned_revision(entry) or hf.get("revision")
            tensor, reference = hf_reference_tensor(image, hf["repo"], hf["kind"], hf_config, revision)
        elif path.name in TIMM_MODELS:
            tensor, reference = timm_reference_tensor(image, TIMM_MODELS[path.name])
        else:
            raise RuntimeError(
                f"{path.name}: no known reference pipeline; add it to HF_PROCESSORS or TIMM_MODELS"
            )
        fixture["reference"] = reference

        if not spec.get("width"):
            width = int(tensor.shape[-1])
        if not spec.get("height"):
            height = int(tensor.shape[-2])

        reference_interpolation = None
        if reference["kind"] == "timm_transform":
            reference_interpolation = reference["config"]["interpolation"]
        elif reference.get("config", {}).get("resample") == 2:
            reference_interpolation = "bilinear"
        elif reference.get("config", {}).get("data_config", {}).get("interpolation"):
            reference_interpolation = reference["config"]["data_config"]["interpolation"]
        if reference_interpolation and reference_interpolation != interpolation:
            print(
                f"  WARNING: manifest interpolation {interpolation!r} differs from the "
                f"reference {reference_interpolation!r}"
            )

        fixture["preprocess"] = preprocess_record(
            tensor,
            width,
            height,
            mean,
            std,
            bool(spec.get("normalize")),
            crop_pct,
            interpolation,
            {"generator": "gradient-checkerboard-v1", "width": image.width, "height": image.height},
        )
        print_processor_config(reference, hf_config)

        session = ort.InferenceSession(str(path), providers=["CPUExecutionProvider"])
        feed = {session.get_inputs()[0].name: tensor.astype(np.float32)}
        raw_outputs = session.run(None, feed)
        for (name, dims, _), raw in zip(outputs, raw_outputs):
            stats = classify_outputs(raw)
            activation = "none" if stats["is_probability"] else "softmax"
            fixture["activation"] = activation
            probabilities = activate(raw, activation)
            print(
                f"  {name}: shape={list(raw.shape)} -> "
                f"{'probabilities' if stats['is_probability'] else 'logits'} "
                f"(sum={stats['sum']:.6f}, min={stats['min']:.6f}, max={stats['max']:.6f}), "
                f"manifest activation={activation}"
            )
            output_record = {
                "shape": list(raw.shape),
                "stats": {"min": stats["min"], "max": stats["max"], "sum": stats["sum"]},
                "values": [round(float(value), 6) for value in raw.reshape(-1)],
            }
            if labels and len(labels) == probabilities.size:
                output_record["scores"] = {
                    label: round(float(score), 6) for label, score in zip(labels, probabilities)
                }
            fixture["outputs"][name] = output_record

    fixtures_dir.mkdir(parents=True, exist_ok=True)
    fixture_path = fixtures_dir / f"{path.name}.json"
    fixture_path.write_text(json.dumps(fixture, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(f"  fixture: {fixture_path} ({fixture_path.stat().st_size} bytes)")
    return fixture


def main(argv: list[str] | None = None) -> int:
    args = parse_args(argv)
    models_dir = args.dir
    if not models_dir.is_dir():
        print(f"error: model directory not found: {models_dir}", file=sys.stderr)
        return 1
    onnx_files = sorted(models_dir.glob("*.onnx"))
    if not onnx_files:
        print(f"error: no .onnx files in {models_dir}", file=sys.stderr)
        return 1

    manifest = load_manifest(args.manifest)
    for path in onnx_files:
        try:
            inspect_file(path, manifest, args.fixtures, not args.no_hf_config)
        except Exception as err:  # noqa: BLE001 - top-level report
            print(f"error: {path.name}: {err}", file=sys.stderr)
            return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())

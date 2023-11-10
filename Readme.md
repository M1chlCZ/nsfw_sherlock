# NSFW Sherlock
__Build with GO and TensorFlow__

[![License: AGPL](https://img.shields.io/badge/license-AGPL-blue.svg)](https://)
[![Go Report Card](https://goreportcard.com/badge/github.com/M1chlCZ/nsfw_sherlock)](https://goreportcard.com/report/github.com/M1chlCZ/nsfw_sherlock)
[![GoDoc](https://godoc.org/github.com/M1chlCZ/nsfw_sherlock?status.svg)](https://godoc.org/github.com/M1chlCZ/nsfw_sherlock)
[![GoDoc](https://img.shields.io/docker/v/m1chl/nsfw-sherlock/latest?color=green&label=Docker%20Hub&style=flat)](https://hub.docker.com/repository/docker/m1chl/nsfw-sherlock/general)
###
Simple drop-in API for classifying images for NSFW content.

It can determine if picture is NSFW as well as if text on picture is NSFW

###

__Both HTTP and GRPC APIs are supported__

Use docker to run NSFW Sherlock

###
__UPDATE 0.0.0.4:__

Now you can use additional API, which will give you labels (Drawings, Neutral, Hentai, Porn, Sexy) and its values directly. Implemented on both Web and GRPC APIs.
For Web: /pic/labels
For GRPC: Please refer to protofile in ./proto

### 

__Usage with Git Clone:__


    git clone https://github.com/M1chlCZ/nsfw_sherlock nsfw_sherlock
    cd nsfw_sherlock
    docker build -t nsfwsherlock .

    GRPC:
    docker run -e APP_ENV=grpc -p 4000:4000 nsfwsherlock 
    (proto file in /proto)
    
    HTTP:
    docker run -e APP_ENV=http -p 4000:4000 nsfwsherlock
    (POST /pic/check) req: {"base64": "base64 string of image", filename: "image.jpg"}
    res: {
        "status":   "ok", 
        "message":  "success", 
        "nsfwText": true/false, 
        "nsfwPic":  true/false
    }
    
    (POST /pic/labels) req: {"base64": "base64 string of image", filename: "image.jpg"}, 
    res: {
        "status":   "ok",
		"message":  "success",
		"drawings": 0.0,
		"hentai":   0.0,
		"neutral":  0.0,
		"porn":     0.0,
		"sexy":     0.0,
		"nsfwText": true/false,
      },

    ---------------------------------------------

    LOADING YOUR OWN BAD WORDS:
    docker run -e APP_ENV=grpc/http  -v /path/to/your/host/bad_words.txt:/bad_words.txt -p 4000:4000 nsfwsherlock

    If you need tighten up or loosen up NSFW detection rules, you can do so in nsfw/nsfw.go file

#

__Usage with Docker Hub:__


    docker pull m1chl/nsfw-sherlock

    GRPC:
    docker run -e APP_ENV=grpc -p 4000:4000 m1chl/nsfw-sherlock 
    (proto file in /proto)
    
    HTTP:
    docker run -e APP_ENV=http -p 4000:4000 m1chl/nsfw-sherlock
    (POST /pic/check) req: {"base64": "base64 string of image", filename: "image.jpg"}
    res: {
        "status":   "ok", 
        "message":  "success", 
        "nsfwText": true/false, 
        "nsfwPic":  true/false
    }
    
    (POST /pic/labels) req: {"base64": "base64 string of image", filename: "image.jpg"}, 
    res: {
        "status":   "ok",
		"message":  "success",
		"drawings": 0.0,
		"hentai":   0.0,
		"neutral":  0.0,
		"porn":     0.0,
		"sexy":     0.0,
		"nsfwText": true/false,
      },
    

    ---------------------------------------------

    LOADING YOUR OWN BAD WORDS:
    docker run -e APP_ENV=grpc/http  -v /path/to/your/host/bad_words.txt:/bad_words.txt -p 4000:4000 nsfwsherlock

#
__Credits:__

    Model: https://github.com/GantMan/nsfw_model

    Tensorflow-GO: https://github.com/galeone/tfgo

    OCR: https://github.com/otiai10/gosseract




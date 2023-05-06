# NSFW Sherlock
__Build with GO, TensorFlow and luck__

[![License: AGPL](https://img.shields.io/badge/license-AGPL-blue.svg)](https://)
[![Go Report Card](https://goreportcard.com/badge/github.com/ProjectMerge/safe-ordinals-gallery/tree/b2c799ea38685f0ecac47e5e6c09d46d96267c16/middleware)](http://github.com/ProjectMerge/safe-ordinals-gallery/tree/b2c799ea38685f0ecac47e5e6c09d46d96267c16/middleware)
[![GoDoc](https://godoc.org/github.com/M1chlCZ/nsfw_sherlock?status.svg)](https://godoc.org/github.com/M1chlCZ/nsfw_sherlock)
###
API for classifying images for NSFW content.

It can determine if picture is NSFW as well as if text on picture is NSFW

###

__Both HTTP and GRPC APIs are supported__

Use docker to run this, strongly recommended


### 

__Usage:__


    git clone https://github.com/M1chlCZ/nsfw_sherlock nsfw_sherlock
    cd nsfw_sherlock
    docker build -t nsfwsherlock .

    GRPC:
    docker run -e APP_ENV=grpc -p 4000:4000 nsfwsherlock 
    (proto file in /proto)
    
    HTTP:
    docker run -e APP_ENV=http -p 4000:4000 nsfwsherlock
    (POST /pic/check)

    LOADING YOUR BAD WORDS DICT:
    docker run -e APP_ENV=grpc  -v /path/to/your/host/bad_words.txt:/bad_words.txt -p 4000:4000 nsfwsherlock

#
__Credits:__

    Model, snippets of logic: https://github.com/photoprism/photoprism
    Tensorflow-GO: https://github.com/galeone/tfgo
    OCR: https://github.com/otiai10/gosseract




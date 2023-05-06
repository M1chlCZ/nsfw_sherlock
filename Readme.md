# NSFW Sherlock
__Build with GO, TensorFlow and luck__

[![License: AGPL](https://img.shields.io/badge/license-AGPL-blue.svg)](https://)
[![Go Report Card](https://goreportcard.com/badge/github.com/M1chlCZ/nsfw_sherlock)](https://goreportcard.com/report/github.com/M1chlCZ/nsfw_sherlock)
[![GitHub Discussions](https://img.shields.io/badge/ask-%20on%20github-4d6a91.svg)](https://github.com/github.com/M1chlCZ/nsfw_sherlock/wiki)
[![GoDoc](https://godoc.org/github.com/M1chlCZ/nsfw_sherlock?status.svg)](https://godoc.org/github.com/M1chlCZ/nsfw_sherlock)
###
API for classifying images for NSFW content.

It can determine if picture is NSFW as well as if text on picture is NSFW

###

__Both HTTP and GRPC APIs are supported__

Use docker to run NSFW Sherlock


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

    ---------------------------------------------

    LOADING YOUR OWN BAD WORDS:
    docker run -e APP_ENV=grpc/http  -v /path/to/your/host/bad_words.txt:/bad_words.txt -p 4000:4000 nsfwsherlock

#
__Credits:__

    Model, snippets of logic: https://github.com/photoprism/photoprism

    Tensorflow-GO: https://github.com/galeone/tfgo

    OCR: https://github.com/otiai10/gosseract




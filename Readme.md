# NSFW Sherlock
__Build with GO, TensorFlow and good luck__
###
API for classifying images for NSFW content.

It can determine if picture is NSFW as well as if text on picture is NSFW

###

__Both HTTP and GRPC APIs are supported__

Use docker to run this, otherwise good luck 🫡


### 

__Usage:__


    git clone https://github.com/M1chlCZ/nsfw_sherlock nsfw_sherlock
    cd nsfw_sherlock
    bash nsfw_model
    docker build -t nsfwsherlock .

    GRPC:
    docker run -e APP_ENV=grpc -p 4000:4000 nsfwsherlock 
    (proto file in /proto)
    
    HTTP:
    docker run -e APP_ENV=http -p 4000:4000 nsfwsherlock
    (POST /pic/check)

#
__Credits:__

    Model, snippets of logic: https://github.com/photoprism/photoprism
    Tensorflow-GO: https://github.com/galeone/tfgo
    OCR: https://github.com/otiai10/gosseract




# NSFW Sherlock
__Build with GO, TensorFlow and good luck__
###
API for classifying images if they are NSFW or not, it does not work on NSFL content

Both HTTP and GRPC APIs are supported

Use docker to run this, otherwise good luck™


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


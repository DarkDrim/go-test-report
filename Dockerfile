ARG GO_VERSION=1.22.4

FROM golang:${GO_VERSION}-alpine

# installs GCC, libc-dev, etc
RUN apk add build-base

# makes working with alpine-linux a little easier
RUN apk add --no-cache shadow

RUN apk add --update nodejs npm

# Create a non-privileged user for running the go app
RUN groupadd -r dockeruser && useradd -r -g dockeruser dockeruser

WORKDIR /home/dockeruser

ADD . .

RUN npm install
RUN npm fund
RUN npm run test

# Our Makefile version is GNU Make which alpine uses by default
RUN make genbuild
RUN go test -v

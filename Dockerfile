FROM golang:1.16.4-alpine

WORKDIR /app
ADD ./ /app

RUN apk update 
RUN apk add \
	protobuf \
	make

RUN make app

CMD ["./app", "-s"]

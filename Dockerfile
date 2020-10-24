FROM golang:alpine

WORKDIR /crawler
COPY cmd/crawler.go .

RUN go build crawler.go

ENTRYPOINT ["./crawler"]
CMD ["--help"]
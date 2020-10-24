FROM golang:alpine AS builder

WORKDIR /crawler

COPY crawler/go.mod .
COPY crawler/go.sum .
RUN go mod download

COPY crawler/ .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o binary

####
FROM scratch

COPY --from=builder etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

COPY --from=builder /crawler/binary /crawler

ENTRYPOINT ["/crawler"]
CMD ["--help"]
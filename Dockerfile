# *Important:* you need to run `dep ensure --vendor-only` before building this image
# Build the manager binary
FROM golang:1.10.3 as builder

# Copy in the go src
WORKDIR /go/src/github.com/DevFactory/smartnat
COPY pkg/    pkg/
COPY cmd/    cmd/
COPY vendor/ vendor/

# Build
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -o smartnat-manager github.com/DevFactory/smartnat/cmd/manager

# Copy the controller-manager into a thin image
FROM alpine:3.9
WORKDIR /root/
COPY --from=builder /go/src/github.com/DevFactory/smartnat/smartnat-manager .
ENTRYPOINT ["./smartnat-manager"]

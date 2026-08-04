# Builds gqlhash-proxy from the repository, so the playground runs the working tree
# rather than a release. The build context is the repository root.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /gqlhash-proxy ./cmd/gqlhash-proxy

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /gqlhash-proxy /gqlhash-proxy
EXPOSE 8080 9090
ENTRYPOINT ["/gqlhash-proxy"]

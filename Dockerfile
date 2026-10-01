FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /compost ./cmd/compost

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /compost /compost
ENV COMPOST_LISTEN=:8080 COMPOST_CACHE_DIR=/heap
VOLUME /heap
EXPOSE 8080
ENTRYPOINT ["/compost"]

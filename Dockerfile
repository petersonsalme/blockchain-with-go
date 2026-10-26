# syntax=docker/dockerfile:1
ARG GO_VERSION=1.26.10

FROM golang:${GO_VERSION} AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# bwg node needs a .env in its working directory; ship an empty one so that
# configuration comes only from the environment (docker run -e).
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/bwg ./cmd/bwg \
 && mkdir -p /out/app /out/data \
 && touch /out/app/.env

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/bwg /usr/local/bin/bwg
COPY --from=build --chown=65532:65532 /out/app /app
COPY --from=build --chown=65532:65532 /out/data /data
WORKDIR /app
ENV PORT=8080
EXPOSE 8080
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/bwg"]
CMD ["node"]

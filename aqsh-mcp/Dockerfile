FROM golang:1.26.8-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/aqsh-mcp ./cmd/aqsh-mcp

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/aqsh-mcp /aqsh-mcp
EXPOSE 8090
ENTRYPOINT ["/aqsh-mcp"]
CMD ["-config", "/etc/aqsh-mcp/config.yaml"]

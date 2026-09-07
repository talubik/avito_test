FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/demo-restaurant ./cmd/api

FROM alpine:3.22
RUN addgroup -S app && adduser -S -G app app
COPY --from=build /out/demo-restaurant /app/demo-restaurant
USER app
EXPOSE 8081
ENTRYPOINT ["/app/demo-restaurant"]

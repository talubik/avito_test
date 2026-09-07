FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/kitchen ./cmd/api \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/worker ./cmd/worker

FROM build AS test
RUN apk add --no-cache build-base
ENV CGO_ENABLED=1

FROM alpine:3.22
RUN addgroup -S app && adduser -S -G app app
COPY --from=build /out/kitchen /out/worker /app/
USER app
EXPOSE 8080
ENTRYPOINT ["/app/kitchen"]

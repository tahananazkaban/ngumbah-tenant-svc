FROM golang:1.27.2-alpine3.24 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Static binary so it runs on a distroless base without libc
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/tenant-svc ./cmd/tenant-svc

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /out/tenant-svc /tenant-svc
EXPOSE 50051
HEALTHCHECK --interval=5s --timeout=3s --retries=10 CMD ["/tenant-svc", "health"]
ENTRYPOINT ["/tenant-svc"]
CMD ["serve"]

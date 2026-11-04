FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod *.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /dispatcher .

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /dispatcher /dispatcher
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/dispatcher"]

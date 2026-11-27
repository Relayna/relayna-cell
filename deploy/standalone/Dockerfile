FROM golang:1.26.0 AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY cmd/cell-task ./cmd/cell-task
COPY pkg ./pkg
RUN CGO_ENABLED=0 go build -trimpath -o /bin/cell-task ./cmd/cell-task

FROM scratch
LABEL org.opencontainers.image.description="Trusted launcher for a local Relayna Cell operator socket"
COPY --from=build /bin/cell-task /cell-task
ENTRYPOINT ["/cell-task"]

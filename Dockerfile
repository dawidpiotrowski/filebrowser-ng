FROM node:24-alpine AS frontend
WORKDIR /src/frontend
RUN npm install --global pnpm@10.33.4
COPY frontend/package.json frontend/pnpm-lock.yaml frontend/pnpm-workspace.yaml ./
COPY frontend/patches ./patches
RUN pnpm install --frozen-lockfile
COPY frontend/ ./
RUN pnpm run build

FROM golang:1.27.3-alpine AS backend
WORKDIR /src
RUN apk add --no-cache ca-certificates
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=frontend /src/frontend/dist ./frontend/dist
RUN CGO_ENABLED=0 go test ./...
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/filebrowser-ng . && \
    mkdir -p /runtime/srv /runtime/database && \
    chown -R 65532:65532 /runtime

FROM scratch
COPY --from=backend /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=backend /out/filebrowser-ng /filebrowser-ng
COPY --from=backend --chown=65532:65532 /runtime/ /
USER 65532:65532
WORKDIR /srv
EXPOSE 8080
VOLUME ["/srv", "/database"]
ENTRYPOINT ["/filebrowser-ng"]
CMD ["--root=/srv", "--database=/database/filebrowser.db", "--address=0.0.0.0", "--port=8080"]

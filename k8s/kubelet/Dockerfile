from golang:alpine as builder

workdir /go/src

copy go.mod go.sum ./
run go mod download

copy . .
run cd kubelet && CGO_ENABLED=0 go build -v ./

from scratch
copy --from=builder /go/src/kubelet/kubelet /bin/kubelet
entrypoint ["/bin/kubelet"]
cmd ["-p", "8080"]

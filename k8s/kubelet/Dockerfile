from golang:alpine as builder

workdir /go/src

copy go.mod go.sum ./
run go mod download

copy . .
run cd kubelet && go build -v ./

from alpine
copy --from=builder /go/src/kubelet/kubelet /bin/kubelet
entrypoint ["/bin/kubelet"]
cmd ["-p", "8080"]

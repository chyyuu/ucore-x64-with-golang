CURRENT=`pwd`

export GOHOSTOS=linux
export GOHOSTARCH=amd64
export GOOS=ucoresmp
export GOARCH=amd64
export GOROOT=$CURRENT
export GOBIN=$GOROOT/bin
export PATH=$GOROOT/bin:$PATH
export CGO_ENABLED=0

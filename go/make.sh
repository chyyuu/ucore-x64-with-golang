#!/bin/bash

#Get current director;
CURRENT=`pwd`

export GOHOSTOS=linux
export GOHOSTARCH=amd64
export GOOS=ucoresmp
export GOARCH=amd64
export GOROOT=$CURRENT
export PATH=$GOROOT/bin:$PATH
export CGO_ENABLED=0
export GOBIN=$GOROOT/bin

build_go()
{
	cd $GOROOT/src
	. ./make.bash
}

diff_go()
{
	original_go="`readlink "$CURRENT/../../go" -f`"
	diff "$original_go/src" "$CURRENT/src" -r -x "Make.deps" -q
}

clean_go()
{
	cd $GOROOT/src
	. ./clean.bash
}

compile_go()
{
	cd "$GOROOT/testsuit"
	6g -o "$1.6" "$1.go" && 6l -o "$1.out" "$1.6"
	mv "$1.out" "$GOROOT/../ucore/src/user-ucore/_initial/"
	rm "$1.6"
    cd $GOROOT
}

rebuild_pkg()
{
	cd "$GOROOT/src/pkg/$1"
	make clean
	make
}

case $1 in
    all)
        clean_go
        build_go
	    rm "$GOROOT/../ucore/obj/sfs.img" 2> /dev/null
        compile_go hw1
        compile_go hw2
        compile_go peter
        echo " exec all finished"
        exit
        ;;
	clean)
		clean_go
		exit
		;;
	diff)
		diff_go
		exit
		;;
	compile)
	    rm "$GOROOT/../ucore/obj/sfs.img" 2> /dev/null
		compile_go $2
		exit
		;;
	make | build)
		build_go
		exit
		;;
	rebuild)
		rebuild_pkg $2
		exit
		;;
	help)
		echo "Usage:"
		echo "    clean: make clean;"
		echo "    compile %s: compile %s.go and put it in ucore's _initial;"
		echo "    make, build: make the go compiler;"
        echo "    all: clean;build;compile hw1,hw2,peter"
		;;
	'')
		;;	
	*)
		echo "Unrecognized parameter."
		exit
		;;
esac



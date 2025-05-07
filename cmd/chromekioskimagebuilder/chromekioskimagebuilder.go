package main

import "fmt"

func main() {
	// TODO

	fmt.Println(`
mkdir -p deb
cd deb
wget https://dl.google.com/linux/direct/google-chrome-stable_current_amd64.deb
cd ..

docker build -t "chromekiosk-dist" -f Dockerfile --target=dist --progress=plain --squash=true .

C="$(docker create "chromekiosk-dist")"
docker export "$C" | tar xv image.squashfs
echo $C
docker rm "$C"
`)
}

package main

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"math/rand"
	"os"
)

// The number of targets to generate.
const numTargets = 1000

// Creates a BUILD that contains numTargets many targets. In total all targets
// create outputs sized numTargets * (numTargets - 1) / 2 KiB.
func main() {
	const GenruleTemplate = `
genrule(
	name = "target_%d",
	outs = ["target_%d.out"],
	cmd = "dd if=/dev/urandom of=$@ bs=10240 count=%d",
)
`

	var buffer bytes.Buffer
	shuffled := rand.Perm(numTargets)
	for _, num := range shuffled {
		num++
		genrule := fmt.Sprintf(GenruleTemplate, num, num, num)
		buffer.WriteString(genrule)
	}
	err := ioutil.WriteFile("BUILD", buffer.Bytes(), 0666)
	if err != nil {
		os.Exit(1)
	}
}

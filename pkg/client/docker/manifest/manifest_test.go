package docker

import (
	"context"
	"testing"
)

func TestDigest(t *testing.T) {
	tests := map[string]struct {
		repo  string
		image  string
		tag  string
		expDigest string
	}{
		"works for a sample multiarch image": {
			repo:  "library",
			image: "eclipse-mosquitto",
			tag: "2.0.14",
			expDigest: "sha256:43b90568c315eeae5cbdcd75ef41aa109aef2170bc714443fe3586d565783d18",
		},
		"works for a sample singlearch image": {
			repo:  "n8nio",
			image: "n8n",
			tag: "0.123.1-rpi",
			expDigest: "sha256:9fa8ff78d46b8d55548d3d3e2bac7fb83c1b7605cafa2899ea2eb3b1550c230e",
		},
	}

	handler, err := New(Options{})
	
	if err != nil {
		t.Fatal(err)
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			digest, err := handler.Digest(context.TODO(), test.repo, test.image, test.tag)

			if err != nil {
				t.Fatal(err)
			}
		
			if digest != test.expDigest {
				t.Errorf("%s, %s, %s: unexpected digest, exp=%s got=%s",
					test.repo, test.image, test.tag, test.expDigest, digest)
			}
		})
	}
}

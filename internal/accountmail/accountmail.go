// Package accountmail holds the branded account-mail templates.
package accountmail

import (
	"embed"
	"fmt"
)

//go:embed layout.html confirmation.html
var files embed.FS

// LogoURL is the absolute public URL of the mark in every mail.
const LogoURL = "https://api.ascomply.com/emails/mark.png"

// Template returns layout.html plus <name>.html as one html/template source.
func Template(name string) ([]byte, error) {
	if name != "confirmation" {
		return nil, fmt.Errorf("accountmail: unknown mail %q", name)
	}
	layout, err := files.ReadFile("layout.html")
	if err != nil {
		return nil, err
	}
	mail, err := files.ReadFile(name + ".html")
	if err != nil {
		return nil, err
	}
	return append(layout, mail...), nil
}

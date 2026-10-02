// embed.go — встраивание веб-интерфейса в исполняемый файл.
package main

import _ "embed"

//go:embed web/index.html
var indexHTML string

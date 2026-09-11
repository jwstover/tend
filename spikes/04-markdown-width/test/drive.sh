#!/bin/sh
# Runs the whole spike: Go reference outputs, then the three Elixir checks.
#
#   test/drive.sh          everything, at the detail pane's usual 80 columns
#   test/drive.sh 60       same, at 60 columns
set -eu
cd "$(dirname "$0")/.."
width=${1:-80}
mkdir -p tmp

echo "#### gocheck: x/ansi reference and glamour render"
(cd gocheck && go build -o gocheck .)
gocheck/gocheck widths > tmp/go-widths.json
gocheck/gocheck glamour fixtures/body-206.md "$width" > "tmp/glamour-$width.ans"
cp "tmp/glamour-$width.ans" tmp/glamour-80.ans 2>/dev/null || true
echo "wrote tmp/go-widths.json tmp/glamour-$width.ans"

echo
echo "#### markdown: Breeze.Markdown and <.markdown> vs glamour"
mix run -e 'Spike4.main(System.argv())' -- markdown "$width"

echo
echo "#### ast: earmark_parser on the same body"
mix run -e 'Spike4.main(System.argv())' -- ast

echo
echo "#### width: BackBreeze vs x/ansi"
mix run -e 'Spike4.main(System.argv())' -- width tmp/go-widths.json

echo
echo "#### gwidth: the prototype grapheme-cluster width rule vs x/ansi"
mix run -e 'Spike4.main(System.argv())' -- gwidth tmp/go-widths.json

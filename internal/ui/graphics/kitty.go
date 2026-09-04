package graphics

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"os"
	"strings"

	// Registered for their decoders: a Telegram photo arrives as JPEG, and
	// stickers and animations as WebP or MP4 — the first two decode here,
	// the rest are refused by Encode rather than guessed at.
	_ "image/gif"
	_ "image/jpeg"
)

// Kitty graphics, in the shape a TUI needs.
//
// The protocol is a family of escape sequences carrying key=value pairs and
// a base64 payload. What is used here is its simplest useful corner: transmit
// an image and place it at the cursor, sized in terminal cells, in one go
// (a=T), with the payload split into chunks the terminal reassembles.
//
// Two decisions are worth naming. The image is re-encoded as PNG rather than
// sent as the original JPEG, because f=100 (PNG) is the one compressed format
// every implementation accepts — kitty documents PNG and raw pixels only.
// And it is scaled down before encoding rather than sent whole and scaled by
// the terminal: a 4000×3000 phone photo is several megabytes of base64 on a
// wire that may be an ssh session, to fill a box twenty cells wide.

// maxChunk is the payload size per escape sequence. 4096 base64 bytes is what
// the protocol documents as the maximum a terminal must accept.
const maxChunk = 4096

// Image is a picture ready to be drawn: the encoded payload plus how many
// terminal rows it will occupy, which the caller needs in order to reserve
// space for it in a layout the terminal knows nothing about.
type Image struct {
	// ID is the image number the terminal files this picture under. It
	// exists so that redrawing is idempotent — see kittyEscape.
	ID uint32
	// Escape is the full sequence to write at the point the image should
	// appear.
	Escape string
	// Rows is how many terminal rows the image occupies. The caller must
	// leave that many rows empty after writing Escape, or the next line of
	// text is drawn over the picture.
	Rows int
	// Cols is the width in cells, for the same reason.
	Cols int
}

// Encode reads the image at path and returns it sized to fit within
// maxCols × maxRows terminal cells, preserving aspect ratio.
//
// cellAspect is the height-to-width ratio of one terminal cell — roughly 2 on
// every terminal anyone uses, because characters are taller than they are
// wide. Without it every image comes out squashed to half its height, which
// is the single most common bug in terminal image code.
func Encode(path string, id uint32, maxCols, maxRows int, cellAspect float64) (Image, error) {
	if id == 0 {
		// 0 is not a valid image number in the protocol, and a terminal
		// handed one has to guess. Guessing here would mean every picture
		// sharing an id and deleting each other.
		return Image{}, fmt.Errorf("graphics: image id must not be zero")
	}
	if maxCols < 1 || maxRows < 1 {
		return Image{}, fmt.Errorf("graphics: no room to draw (%dx%d cells)", maxCols, maxRows)
	}
	if cellAspect <= 0 {
		cellAspect = 2
	}

	f, err := os.Open(path) //nolint:gosec // the path comes from the download store, not from message content
	if err != nil {
		return Image{}, fmt.Errorf("graphics: open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	src, _, err := image.Decode(f)
	if err != nil {
		return Image{}, fmt.Errorf("graphics: decode %s: %w", path, err)
	}

	cols, rows := fitCells(src.Bounds().Dx(), src.Bounds().Dy(), maxCols, maxRows, cellAspect)
	// Pixels per cell are unknown — the terminal knows, the program does
	// not, and asking costs a round trip. 10×20 is close enough to every
	// common font at a normal size, and being wrong only means the image is
	// re-scaled by the terminal to the cell box it was told to fill.
	const pxPerColGuess, pxPerRowGuess = 10, 20
	scaled := scale(src, cols*pxPerColGuess, rows*pxPerRowGuess)

	var buf bytes.Buffer
	if err := png.Encode(&buf, scaled); err != nil {
		return Image{}, fmt.Errorf("graphics: encode png: %w", err)
	}
	return Image{
		ID:     id,
		Escape: kittyEscape(buf.Bytes(), id, cols, rows),
		Rows:   rows,
		Cols:   cols,
	}, nil
}

// fitCells picks the cell box an image of w×h pixels should occupy, never
// exceeding the maximum in either direction and never scaling up — an image
// smaller than the box is drawn at its own size rather than blown up into a
// blur.
func fitCells(w, h, maxCols, maxRows int, cellAspect float64) (cols, rows int) {
	if w <= 0 || h <= 0 {
		return 1, 1
	}
	// Convert the pixel aspect into cells: one cell is cellAspect times
	// taller than it is wide, so a square image needs half as many rows as
	// columns.
	ratio := float64(h) / float64(w) / cellAspect

	cols = maxCols
	rows = int(float64(cols)*ratio + 0.5)
	if rows > maxRows {
		rows = maxRows
		cols = int(float64(rows)/ratio + 0.5)
	}
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	return cols, rows
}

// scale resamples src to w×h with a box filter.
//
// Hand-rolled rather than pulled from golang.org/x/image: the dependency
// would be the first one added to this module for a cosmetic path, and a box
// filter over a downscale is what that package's NearestNeighbor and
// ApproxBiLinear are for. Downscaling is the only direction that happens
// here, and averaging the source pixels that land in each destination pixel
// is exactly right for it — better than nearest-neighbour, which turns fine
// detail into noise.
func scale(src image.Image, w, h int) image.Image {
	b := src.Bounds()
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	if w >= b.Dx() && h >= b.Dy() {
		return src
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	xRatio := float64(b.Dx()) / float64(w)
	yRatio := float64(b.Dy()) / float64(h)

	for y := 0; y < h; y++ {
		y0 := b.Min.Y + int(float64(y)*yRatio)
		y1 := b.Min.Y + int(float64(y+1)*yRatio)
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for x := 0; x < w; x++ {
			x0 := b.Min.X + int(float64(x)*xRatio)
			x1 := b.Min.X + int(float64(x+1)*xRatio)
			if x1 <= x0 {
				x1 = x0 + 1
			}
			var r, g, bl, a, n uint64
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					cr, cg, cb, ca := src.At(sx, sy).RGBA()
					r += uint64(cr)
					g += uint64(cg)
					bl += uint64(cb)
					a += uint64(ca)
					n++
				}
			}
			if n == 0 {
				continue
			}
			dst.Set(x, y, rgba16{r / n, g / n, bl / n, a / n})
		}
	}
	return dst
}

// rgba16 carries the averaged 16-bit-per-channel values back into the image
// package's colour model without a rounding trip through 8 bits.
//
// The accumulator is 64-bit because summing a box of pixels overflows 32 bits
// on anything but a tiny box; the values handed back are the averages, each
// of which is by construction a colour channel and therefore fits. clamp16
// says so to the compiler and to gosec rather than asserting it in a comment
// nobody checks.
type rgba16 struct{ r, g, b, a uint64 }

func (c rgba16) RGBA() (r, g, b, a uint32) {
	return clamp16(c.r), clamp16(c.g), clamp16(c.b), clamp16(c.a)
}

func clamp16(v uint64) uint32 {
	const maxChannel = 0xffff
	if v > maxChannel {
		return maxChannel
	}
	return uint32(v)
}

// kittyEscape builds the transmit-and-display sequence for a PNG payload.
//
// The payload is split into chunks because the protocol requires it: a
// terminal is only obliged to accept 4096 base64 bytes per escape. m=1 says
// "more chunks follow", m=0 ends the transmission — and every chunk after the
// first repeats no keys, which is what implementations expect.
//
// It opens by deleting the placements of this image id, and that is the whole
// reason ids exist here. A picture is not part of the text grid: the terminal
// anchors it where it was drawn and redrawing the line underneath does not
// move it. So a thread that scrolls — or simply repaints the line when the
// cursor moves — writes the escape at a new row and leaves the old copy
// behind, and the user watches their photo multiply down the pane. Deleting
// by id first (d=i keeps the stored image, only the placements go) makes
// every redraw idempotent, and it rides in the same string as the placement
// so a renderer that skips an unchanged line skips both together.
//
// q=2 silences the terminal's acknowledgement. Without it every draw sends a
// reply back up stdin, which the TUI's input parser then has to swallow.
func kittyEscape(png []byte, id uint32, cols, rows int) string {
	payload := base64.StdEncoding.EncodeToString(png)

	var b strings.Builder
	fmt.Fprintf(&b, "\x1b_Ga=d,d=i,i=%d,q=2\x1b\\", id)
	first := true
	for len(payload) > 0 {
		chunk := payload
		if len(chunk) > maxChunk {
			chunk = chunk[:maxChunk]
		}
		payload = payload[len(chunk):]
		more := 0
		if len(payload) > 0 {
			more = 1
		}
		b.WriteString("\x1b_G")
		if first {
			// a=T transmit and display, f=100 PNG, c/r the cell box to fit
			// into, C=1 so the cursor does not move — the caller is laying
			// the rows out itself and a moved cursor would double-count
			// them.
			fmt.Fprintf(&b, "a=T,f=100,i=%d,c=%d,r=%d,C=1,q=2,m=%d", id, cols, rows, more)
			first = false
		} else {
			fmt.Fprintf(&b, "m=%d", more)
		}
		b.WriteByte(';')
		b.WriteString(chunk)
		b.WriteString("\x1b\\")
	}
	return b.String()
}

// DeleteAll is the escape that removes every image the terminal is holding
// for this program.
//
// It exists because images are not part of the text grid: a redraw that
// paints spaces where a picture was leaves the picture there. Anything that
// clears the screen — closing the pane, switching chats — has to say so
// explicitly.
const DeleteAll = "\x1b_Ga=d,d=A,q=2\x1b\\"

// DeleteImage is the escape that removes one picture, by the id it was drawn
// with. Used when the user closes a single image and the rest stay.
//
// d=I rather than d=i: the stored image goes too. Keeping it would save a
// retransmission if the same picture is opened again, at the cost of holding
// every photo the session ever showed in the terminal's memory.
func DeleteImage(id uint32) string {
	return fmt.Sprintf("\x1b_Ga=d,d=I,i=%d,q=2\x1b\\", id)
}

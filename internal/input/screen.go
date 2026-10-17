package input

import (
	"fmt"
	"image"

	"github.com/jezek/xgb/xproto"
)

// Screenshot — снимок области дисплея (координаты корня). Только для калибровки и отладки:
// решения бот принимает по состоянию из мода, не по картинке.
func (in *Input) Screenshot(r Rect) (*image.RGBA, error) {
	img := image.NewRGBA(image.Rect(0, 0, r.W, r.H))
	const strip = 64 // строк за запрос: ответ X-сервера ограничен по размеру
	for y := 0; y < r.H; y += strip {
		h := min(strip, r.H-y)
		rep, err := xproto.GetImage(in.conn, xproto.ImageFormatZPixmap, xproto.Drawable(in.root),
			int16(r.X), int16(r.Y+y), uint16(r.W), uint16(h), 0xffffffff).Reply()
		if err != nil {
			return nil, fmt.Errorf("input: снимок: %w", err)
		}
		if len(rep.Data) < r.W*h*4 {
			return nil, fmt.Errorf("input: снимок: %d байт, ждали %d (глубина %d)", len(rep.Data), r.W*h*4, rep.Depth)
		}
		// ZPixmap 24/32 бит: B, G, R, X
		for i := 0; i < r.W*h; i++ {
			s := rep.Data[i*4 : i*4+4]
			o := img.PixOffset(i%r.W, y+i/r.W)
			img.Pix[o], img.Pix[o+1], img.Pix[o+2], img.Pix[o+3] = s[2], s[1], s[0], 255
		}
	}
	return img, nil
}

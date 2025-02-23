package gui

import (
	"errors"
	"fmt"
	"image"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcutil/hdkeychain"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/skip2/go-qrcode"
	"seedhammer.com/backup"
	"seedhammer.com/bc/urtypes"
	"seedhammer.com/bip32"
	"seedhammer.com/bip39"
	"seedhammer.com/camera"
	"seedhammer.com/engrave"
	"seedhammer.com/font/sh"
	"seedhammer.com/gui/op"
	"seedhammer.com/input"
	"seedhammer.com/mjolnir"
	"seedhammer.com/rgb16"
)

func TestDescriptorScreenError(t *testing.T) {
	ctx := NewContext(newPlatform())
	dupDesc := urtypes.OutputDescriptor{
		Type:      urtypes.P2WSH,
		Threshold: 2,
		Keys:      make([]urtypes.KeyDescriptor, 2),
	}
	fillDescriptor(t, dupDesc, dupDesc.DerivationPath(), 12, 0)
	dupDesc.Keys[1] = dupDesc.Keys[0]
	smallDesc := urtypes.OutputDescriptor{
		Type:      urtypes.P2WSH,
		Threshold: 2,
		Keys:      make([]urtypes.KeyDescriptor, 5),
	}
	fillDescriptor(t, smallDesc, smallDesc.DerivationPath(), 12, 0)
	tests := []struct {
		name string
		desc urtypes.OutputDescriptor
	}{
		{"duplicate key", dupDesc},
		{"small threshold", smallDesc},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scr := &DescriptorScreen{
				Descriptor: test.desc,
			}
			ctxButton(ctx, input.Button3)
			scr.Layout(ctx, op.Ctx{}, image.Point{})
			if scr.warning == nil {
				t.Fatal("DescriptorScreen accepted invalid descriptor")
			}
		})
	}
}

func TestValidateDescriptor(t *testing.T) {
	// Duplicate key.
	dup := urtypes.OutputDescriptor{
		Type:      urtypes.P2WSH,
		Threshold: 1,
		Keys:      make([]urtypes.KeyDescriptor, 2),
	}
	fillDescriptor(t, dup, dup.DerivationPath(), 12, 0)
	dup.Keys[1] = dup.Keys[0]

	// Threshold too small.
	smallDesc := urtypes.OutputDescriptor{
		Type:      urtypes.P2WSH,
		Threshold: 2,
		Keys:      make([]urtypes.KeyDescriptor, 5),
	}
	fillDescriptor(t, smallDesc, smallDesc.DerivationPath(), 12, 0)

	// Non-standard derivation path.
	nonStandard := urtypes.OutputDescriptor{
		Type:      urtypes.P2WSH,
		Threshold: 2,
		Keys:      make([]urtypes.KeyDescriptor, 2),
	}
	fillDescriptor(t, nonStandard, nil, 12, 0)

	tests := []struct {
		name string
		desc urtypes.OutputDescriptor
		err  error
	}{
		{"duplicate key", dup, new(errDuplicateKey)},
		{"threshold too small", smallDesc, backup.ErrDescriptorTooLarge},
		{"non-standard path", nonStandard, new(errNonstandardDerivation)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateDescriptor(test.desc)
			if err == nil {
				t.Fatal("validateDescriptor accepted an unsupported descriptor")
			}
			if !errors.Is(err, test.err) {
				t.Fatalf("validateDescriptor returned %v, expected %v", err, test.err)
			}
		})
	}
}

func TestMainScreen(t *testing.T) {
	scr := new(MainScreen)
	p := newPlatform()
	ctx := NewContext(p)

	frame := func() {
		scr.Layout(ctx, op.Ctx{}, image.Point{}, nil)
	}
	// Select multisig, scan SeedQR.
	ctxButton(ctx, input.Right, input.Button3)
	// Test sd card warning.
	frame()
	if scr.sdcard.warning == nil {
		t.Fatal("MainScreen ignored SD card inserted")
	}
	ctx.NoSDCard = true
	frame()
	if scr.sdcard.warning != nil {
		t.Fatal("MainScreen ignored SD card ejected")
	}
	frame()
	ctxQR(t, p, frame, "011513251154012711900771041507421289190620080870026613431420201617920614089619290300152408010643")
	if scr.warning == nil {
		t.Fatal("MainScreen accepted invalid data for a wallet descriptor")
	}
}

func TestDescriptorScreen(t *testing.T) {
	scr := &DescriptorScreen{
		Descriptor: twoOfThree.Descriptor,
	}
	ctx := NewContext(newPlatform())

	// Accept seed, select 12 words.
	ctxButton(ctx, input.Button3, input.Button3)
	if ctx.EnableSeedScan {
		// Select keyboard input.
		ctxButton(ctx, input.Button3)
	}

	// Enter seed not part of the descriptor.
	mnemonic := make(bip39.Mnemonic, 12)
	for i := range mnemonic {
		mnemonic[i] = bip39.RandomWord()
	}
	mnemonic = mnemonic.FixChecksum()
	for _, w := range mnemonic {
		ctxString(ctx, strings.ToUpper(bip39.LabelFor(w)))
		ctxButton(ctx, input.Button2)
	}
	// Accept seed.
	ctxButton(ctx, input.Button3)
	scr.Layout(ctx, op.Ctx{}, image.Point{})
	if scr.warning == nil {
		t.Fatal("a non-participating seed was accepted")
	}
}

func TestEngraveScreenCancel(t *testing.T) {
	p := newPlatform()
	ctx := NewContext(p)
	scr, err := NewEngraveScreen(ctx, twoOfThree.Descriptor, twoOfThree.Mnemonic, "")
	if err != nil {
		t.Fatal(err)
	}

	// Back.
	ctxButton(ctx, input.Button1)
	// Hold confirm.
	ctxPress(ctx, input.Button3)
	done := scr.Layout(ctx, op.Ctx{}, image.Point{})
	if done {
		t.Error("exited screen without confirmation")
	}
	p.timeOffset += confirmDelay
	done = scr.Layout(ctx, op.Ctx{}, image.Point{})
	if !done {
		t.Error("failed to exit screen")
	}
}

func TestEngraveScreenError(t *testing.T) {
	nonstdPath := []uint32{
		hdkeychain.HardenedKeyStart + 86,
		hdkeychain.HardenedKeyStart + 0,
		hdkeychain.HardenedKeyStart + 0,
	}
	tests := []struct {
		name      string
		threshold int
		keys      int
		path      []uint32
		err       error
	}{
		{"threshold too small", 1, 5, nonstdPath, backup.ErrDescriptorTooLarge},
	}
	for i, test := range tests {
		name := fmt.Sprintf("%d-%d-of-%d", i, test.threshold, test.keys)
		t.Run(name, func(t *testing.T) {
			ctx := NewContext(newPlatform())
			desc := urtypes.OutputDescriptor{
				Type:      urtypes.P2WSH,
				Threshold: test.threshold,
				Keys:      make([]urtypes.KeyDescriptor, test.keys),
			}
			mnemonic := fillDescriptor(t, desc, test.path, 12, 0)
			_, err := NewEngraveScreen(ctx, desc, mnemonic, "")
			if err == nil {
				t.Fatal("invalid descriptor succeeded")
			}
			if !errors.Is(err, test.err) {
				t.Fatalf("got error %v, expected %v", err, test.err)
			}
		})
	}
}

func TestEngraveScreenConnectionError(t *testing.T) {
	p := newPlatform()
	p.engrave.closed = make(chan []mjolnir.Cmd, 1)
	p.engrave.connErr = errors.New("failed to connect")
	ctx := NewContext(p)
	scr, err := NewEngraveScreen(ctx, twoOfThree.Descriptor, twoOfThree.Mnemonic, "")
	if err != nil {
		t.Fatal(err)
	}
	// Press next until connect is reached.
	for scr.instructions[scr.step].Type != ConnectInstruction {
		ctxButton(ctx, input.Button3)
		scr.Layout(ctx, op.Ctx{}, image.Point{})
	}
	// Hold connect.
	ctxPress(ctx, input.Button3)
	if done := scr.Layout(ctx, op.Ctx{}, image.Point{}); done {
		t.Error("exited screen without confirmation")
	}
	p.timeOffset += confirmDelay
	scr.Layout(ctx, op.Ctx{}, image.Point{})
	if scr.engrave.warning == nil {
		t.Fatal("engraver error did not propagate to screen")
	}
	// Dismiss error.
	ctxButton(ctx, input.Button3)
	// Successfully connect, but fail during engraving.
	p.engrave.connErr = nil
	p.engrave.ioErr = errors.New("error during engraving")
	// Hold connect.
	ctxPress(ctx, input.Button3)
	if done := scr.Layout(ctx, op.Ctx{}, image.Point{}); done {
		t.Error("exited screen without confirmation")
	}
	p.timeOffset += confirmDelay
	scr.Layout(ctx, op.Ctx{}, image.Point{})
	if err := scr.engrave.warning; err != nil {
		t.Fatalf("screen reported error for connection success: %v", err)
	}
	for scr.engrave.warning == nil {
		scr.Layout(ctx, op.Ctx{}, image.Point{})
	}
	// Dismiss error and verify screen exits.
	ctxButton(ctx, input.Button3)
	done := scr.Layout(ctx, op.Ctx{}, image.Point{})
	if !done {
		t.Fatal("screen didn't exit after fatal engraver error")
	}
	// Verify device was closed.
	<-p.engrave.closed
}

func TestScanScreenError(t *testing.T) {
	p := newPlatform()
	// Fail on connect.
	p.camera.connErr = errors.New("failed to open camera")
	ctx := NewContext(p)
	scr := &ScanScreen{}
	for scr.camera.err == nil {
		scr.Layout(ctx, op.Ctx{}, image.Point{})
	}
	// Fail during streaming.
	p.camera.connErr = nil
	scr = &ScanScreen{}
	// Connect.
	scr.Layout(ctx, op.Ctx{}, image.Point{})
	go func() {
		<-p.camera.init
		p.camera.in <- camera.Frame{Err: errors.New("error during streaming")}
	}()
	for scr.camera.err == nil {
		scr.Layout(ctx, op.Ctx{}, image.Point{})
	}
}

func TestWordKeyboardcreen(t *testing.T) {
	ctx := NewContext(newPlatform())
	for _, w := range bip39.Wordlist {
		scr := &WordKeyboardScreen{
			Mnemonic: make(bip39.Mnemonic, 1),
		}
		ctxString(ctx, strings.ToUpper(w))
		ctxButton(ctx, input.Button2)
		done := scr.Layout(ctx, op.Ctx{}, &singleTheme, image.Point{})
		if !done {
			t.Errorf("keyboard did not accept %q", w)
		}
		if got := bip39.LabelFor(scr.Mnemonic[0]); got != w {
			t.Errorf("keyboard mapped %q to %q", w, got)
		}
	}
}

func ctxQR(t *testing.T, p *testPlatform, frame func(), qrs ...string) {
	for _, qr := range qrs {
		<-p.camera.init
		p.camera.in <- qrFrame(t, qr)
		delivered := make(chan struct{})
		go func() {
			<-p.camera.out
			close(delivered)
		}()
	loop:
		for {
			select {
			case <-delivered:
				break loop
			default:
				frame()
			}
		}
	}
}

func TestSeedScreenScan(t *testing.T) {
	p := newPlatform()
	ctx := NewContext(p)
	scr := NewEmptySeedScreen(ctx, "")
	frame := func() {
		scr.Layout(ctx, op.Ctx{}, &singleTheme, image.Point{})
	}
	// Select camera.
	ctxButton(ctx, input.Down, input.Button3)
	frame()
	ctxQR(t, p, frame, "011513251154012711900771041507421289190620080870026613431420201617920614089619290300152408010643")
	want, err := bip39.ParseMnemonic("attack pizza motion avocado network gather crop fresh patrol unusual wild holiday candy pony ranch winter theme error hybrid van cereal salon goddess expire")
	if err != nil {
		t.Fatal(err)
	}
	got := scr.Mnemonic
	if !reflect.DeepEqual(got, want) {
		t.Errorf("scanned %v, want %v", got, want)
	}
}

func TestSeedScreenScanInvalid(t *testing.T) {
	p := newPlatform()
	ctx := NewContext(p)
	scr := NewEmptySeedScreen(ctx, "")
	frame := func() {
		scr.Layout(ctx, op.Ctx{}, &singleTheme, image.Point{})
	}
	// Select camera.
	ctxButton(ctx, input.Down, input.Button3)
	frame()
	ctxQR(t, p, frame, "UR:CRYPTO-SEED/OYADGDIYWLAMAEJSZSWDWYTLTIFEENFTLNMNWKBDHNSSRO")
	if scr.warning == nil {
		t.Error("SeedScreen accepted invalid seed")
	}
}

func TestSeedScreenInvalidSeed(t *testing.T) {
	p := newPlatform()
	ctx := NewContext(p)
	scr := NewSeedScreen(ctx, make(bip39.Mnemonic, len(twoOfThree.Mnemonic)))
	copy(scr.Mnemonic, twoOfThree.Mnemonic)
	// Invalidate seed.
	scr.Mnemonic[0] = 0
	// Accept seed.
	ctxButton(ctx, input.Button3)
	_, done := scr.Layout(ctx, op.Ctx{}, &singleTheme, image.Point{})
	if done || scr.warning == nil {
		t.Fatal("invalid seed accepted")
	}
	// Dismiss error.
	ctxButton(ctx, input.Button3)

	// Back.
	ctxButton(ctx, input.Button1)
	// Hold confirm.
	ctxPress(ctx, input.Button3)
	_, done = scr.Layout(ctx, op.Ctx{}, &singleTheme, image.Point{})
	if done {
		t.Error("exited screen without confirmation")
	}
	p.timeOffset += confirmDelay
	_, done = scr.Layout(ctx, op.Ctx{}, &singleTheme, image.Point{})
	if !done {
		t.Error("failed to exit screen")
	}
}

func TestMulti(t *testing.T) {
	if testing.Short() {
		t.Skipf("skipped in -short mode")
	}
	t.Parallel()

	twoOfThreeUR := []string{
		"UR:CRYPTO-OUTPUT/1347-2/LPCFAHFXAOCFADIOCYCMSWIDBYHDQZCYHNOEDWSBMUAMWYOTAHPFFXNECKNBNTHKDEADHLVLJKLYCMAHTAADEHOEADAEAOAEAMTAADDYOTADLOCSDYYKAEYKAEYKAOYKAOCYUTGWPMWYAXAAAYCYCPMTMUKTTAADDLOLAOWKAXHDCLAOZOJPGDLBSABTUYPTDTMEPAKEGRQZIYBWBKTAFTLOJTJKCHGDEORKFXVLRFKSHTJNAAHDCXMDQDGABWMULBONWNSWCXHPGMHPREKIVYGYKODAVTFELNREMDRNISVDBWIDTEWESKAHTAADEHOEADAEAOAEAMTAADDYOTADLOCSDYYKAEYKAEYKAOYKAOCYNDPSTLRTAXAAAYCYMSWPETYTAEVDTLISPT",
		"UR:CRYPTO-OUTPUT/1355-2/LPCFAHGRAOCFADIOCYCMSWIDBYHDQZSRHSEOYKSGAAOXWSOYATEONYNNEHAMNEPMDNHKKEVTTNROHHDRSRGLPDSRFRJSJEHFTOLGBAHLCFJTMHLUDWTEESVWJPTYPFMOTLHTJPJZPTRPCNURVTCMNLTPNTENGMATUYTBTIHPVEWTVTKKCEJKZOHEPLGHKIYLGSESMDKICLTPCMCMTETPBDDRJLJKBZGDECIDFWTECTKKTDKPEEPMCXHNQDRFBYIYKIRSPYTODKROGYHERYIODSWEMELGESFYPTBWMSGEJERSEYHNWZKGISSTLNURDIFSVSDMJPKOMTLABYBGTBTEFNBBYTJPKOCTPYIORDURLRASSKFMTTMKCNFLLNVWWPTSBAGWTTPYMUOELP",
	}

	r := newRunner(t)
	// Select multisig, start scanner.
	r.Button(t, input.Right, input.Button3)
	r.QR(t, twoOfThreeUR...)
	for r.app.scr.desc == nil {
		r.Frame(t)
	}
	// Accept descriptor, select 12 words.
	r.Button(t, input.Button3, input.Button3)
	if r.app.ctx.EnableSeedScan {
		// Select keyboard input.
		r.Button(t, input.Button3)
	}
	for r.app.scr.desc.seed == nil {
		r.Frame(t)
	}
	mnemonic := twoOfThree.Mnemonic
	for _, word := range mnemonic {
		r.String(t, strings.ToUpper(bip39.LabelFor(word)))
		r.Button(t, input.Button2)
	}
	for r.app.scr.desc.seed == nil || !r.app.scr.desc.seed.Mnemonic.Valid() {
		r.Frame(t)
	}
	if got := r.app.scr.desc.seed.Mnemonic; !reflect.DeepEqual(got, mnemonic) {
		t.Fatalf("got seed %v, wanted %v", got, mnemonic)
	}

	// Accept seed, go to engrave.
	r.Button(t, input.Button3)
	for r.app.scr.desc.engrave == nil {
		r.Frame(t)
	}

	testEngraving(t, r, r.app.scr.desc.engrave, twoOfThree.Descriptor, mnemonic, 0)
}

func TestSingle(t *testing.T) {
	if testing.Short() {
		t.Skipf("skipped in -short mode")
	}
	t.Parallel()

	r := newRunner(t)

	// Single sig, 12 words.
	r.Button(t, input.Button3, input.Button3)
	if r.app.ctx.EnableSeedScan {
		// Select keyboard input.
		r.Button(t, input.Button3)
	}
	for r.app.scr.seed == nil {
		r.Frame(t)
	}

	mnemonic := make(bip39.Mnemonic, 12)
	for i := range mnemonic {
		mnemonic[i] = bip39.RandomWord()
	}
	mnemonic = mnemonic.FixChecksum()
	for _, w := range mnemonic {
		r.String(t, strings.ToUpper(bip39.LabelFor(w)))
		r.Button(t, input.Button2)
	}
	for !r.app.scr.seed.Mnemonic.Valid() {
		r.Frame(t)
	}
	if got := r.app.scr.seed.Mnemonic; !reflect.DeepEqual(got, mnemonic) {
		t.Fatalf("got seed %v, wanted %v", got, mnemonic)
	}

	// Accept seed.
	r.Button(t, input.Button3)
	for r.app.scr.engrave == nil {
		r.Frame(t)
	}

	seed := bip39.MnemonicSeed(mnemonic, "")
	desc, ok := singlesigDescriptor(mnemonic, "")
	if !ok {
		t.Fatalf("failed to build single-sig descriptor")
	}
	mk, err := hdkeychain.NewMaster(seed, &chaincfg.MainNetParams)
	if err != nil {
		t.Fatal(err)
	}
	k := desc.Keys[0]
	mfp, xpub, err := bip32.Derive(mk, k.DerivationPath)
	if mfp != k.MasterFingerprint {
		t.Fatalf("expected fingerprint %.8x, got %.8x", mfp, k.MasterFingerprint)
	}
	if want, got := xpub.String(), k.String(); want != got {
		t.Fatalf("got xpub %s, wanted %s", got, want)
	}

	testEngraving(t, r, r.app.scr.engrave, desc, mnemonic, 0)
}

func fillDescriptor(t *testing.T, desc urtypes.OutputDescriptor, path urtypes.Path, seedlen int, keyIdx int) bip39.Mnemonic {
	var mnemonic bip39.Mnemonic
	for i := range desc.Keys {
		m := make(bip39.Mnemonic, seedlen)
		for j := range m {
			m[j] = bip39.Word(i*seedlen + j)
		}
		m = m.FixChecksum()
		seed := bip39.MnemonicSeed(m, "")
		mk, err := hdkeychain.NewMaster(seed, &chaincfg.MainNetParams)
		if err != nil {
			t.Fatal(err)
		}
		mfp, xpub, err := bip32.Derive(mk, path)
		if err != nil {
			t.Fatal(err)
		}
		pub, err := xpub.ECPubKey()
		if err != nil {
			t.Fatal(err)
		}
		desc.Keys[i] = urtypes.KeyDescriptor{
			MasterFingerprint: mfp,
			DerivationPath:    path,
			KeyData:           pub.SerializeCompressed(),
			ChainCode:         xpub.ChainCode(),
			ParentFingerprint: xpub.ParentFingerprint(),
		}
		if i == keyIdx {
			mnemonic = m
		}
	}
	return mnemonic
}

type testPlatform struct {
	input struct {
		in   chan<- input.Event
		init chan struct{}
	}

	camera struct {
		in      chan<- camera.Frame
		out     <-chan camera.Frame
		init    chan struct{}
		connErr error
	}

	engrave struct {
		closed  chan []mjolnir.Cmd
		connErr error
		ioErr   error
	}

	timeOffset time.Duration
	sdcard     chan bool
}

func (t *testPlatform) SDCard() <-chan bool {
	if t.sdcard == nil {
		t.sdcard = make(chan bool, 1)
		t.sdcard <- false
	}
	return t.sdcard
}

func (t *testPlatform) Now() time.Time {
	return time.Now().Add(t.timeOffset)
}

func (t *testPlatform) Dump(path string, r io.Reader) error {
	return errors.New("not implemented")
}

func ctxString(ctx *Context, str string) {
	for _, r := range str {
		ctx.Events(
			input.Event{
				Button:  input.Rune,
				Rune:    r,
				Pressed: true,
			},
		)
	}
}

func ctxPress(ctx *Context, bs ...input.Button) {
	for _, b := range bs {
		ctx.Events(
			input.Event{
				Button:  b,
				Pressed: true,
			},
		)
	}
}

func ctxButton(ctx *Context, bs ...input.Button) {
	for _, b := range bs {
		ctx.Events(
			input.Event{
				Button:  b,
				Pressed: true,
			},
			input.Event{
				Button:  b,
				Pressed: false,
			},
		)
	}
}

func (p *testPlatform) Input(ch chan<- input.Event) error {
	p.input.in = ch
	close(p.input.init)
	return nil
}

type wrappedEngraver struct {
	dev    *mjolnir.Simulator
	closed chan<- []mjolnir.Cmd
	ioErr  error
}

func (w *wrappedEngraver) Read(p []byte) (int, error) {
	n, err := w.dev.Read(p)
	if err == nil {
		err = w.ioErr
	}
	return n, err
}

func (w *wrappedEngraver) Write(p []byte) (int, error) {
	n, err := w.dev.Write(p)
	if err == nil {
		err = w.ioErr
	}
	return n, err
}

func (w *wrappedEngraver) Close() error {
	if w.closed != nil {
		w.closed <- w.dev.Cmds
	}
	return w.dev.Close()
}

func (p *testPlatform) Engraver() (io.ReadWriteCloser, error) {
	if err := p.engrave.connErr; err != nil {
		return nil, err
	}
	sim := mjolnir.NewSimulator()
	return &wrappedEngraver{sim, p.engrave.closed, p.engrave.ioErr}, nil
}

func (p *testPlatform) Camera(dims image.Point, frames chan camera.Frame, out <-chan camera.Frame) (func(), error) {
	if err := p.camera.connErr; err != nil {
		return nil, err
	}
	p.camera.in = frames
	p.camera.out = out
	close(p.camera.init)
	return func() {}, nil
}

type runner struct {
	p      *testPlatform
	app    *App
	frames int
}

func newPlatform() *testPlatform {
	p := &testPlatform{}
	p.input.init = make(chan struct{})
	p.camera.init = make(chan struct{})
	return p
}

func newRunner(t *testing.T) *runner {
	r := &runner{
		p: newPlatform(),
	}
	r.app = NewApp(r.p, r, "")
	r.app.ctx.NoSDCard = true
	return r
}

func (r *runner) Dims() image.Point {
	return image.Point{}
}

func (r *runner) Draw(src *rgb16.Image, sr image.Rectangle) error {
	return nil
}

func (r *runner) String(t *testing.T, str string) {
	t.Helper()
	wait(t, r, r.p.input.init)
	for _, c := range str {
		evt := input.Event{
			Button:  input.Rune,
			Rune:    c,
			Pressed: true,
		}
		deliver(t, r, r.p.input.in, evt)
	}
}

func (r *runner) Frame(t *testing.T) {
	t.Helper()
	r.frames++
	if r.frames > 10000 {
		t.Fatal("test still incomplete after 10000 frames")
	}
	r.app.Frame()
}

func deliver[T any](t *testing.T, r *runner, in chan<- T, v T) {
	t.Helper()
delivery:
	for {
		select {
		case in <- v:
			break delivery
		default:
			r.Frame(t)
		}
	}
}

func wait[T any](t *testing.T, r *runner, out <-chan T) {
	for {
		select {
		case <-out:
			return
		default:
			r.Frame(t)
		}
	}
}

func qrFrame(t *testing.T, content string) camera.Frame {
	qr, err := qrcode.New(content, qrcode.Low)
	if err != nil {
		t.Fatal(err)
	}
	qrImg := qr.Image(512)
	b := qrImg.Bounds()
	frameImg := image.NewYCbCr(b, image.YCbCrSubsampleRatio420)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			off := frameImg.YOffset(x, y)
			r, _, _, _ := qrImg.At(x, y).RGBA()
			frameImg.Y[off] = uint8(r >> 8)
		}
	}
	return camera.Frame{
		Image: frameImg,
	}
}

func (r *runner) QR(t *testing.T, qrs ...string) {
	t.Helper()
	wait(t, r, r.p.camera.init)
	for _, qr := range qrs {
		frame := qrFrame(t, qr)
		deliver(t, r, r.p.camera.in, frame)
		delivered := make(chan struct{})
		go func() {
			<-r.p.camera.out
			close(delivered)
		}()
		wait(t, r, delivered)
	}
}

func (r *runner) Button(t *testing.T, bs ...input.Button) {
	t.Helper()
	wait(t, r, r.p.input.init)
	for _, b := range bs {
		deliver(t, r, r.p.input.in, input.Event{
			Button:  b,
			Pressed: true,
		})
		deliver(t, r, r.p.input.in, input.Event{
			Button:  b,
			Pressed: false,
		})
	}
}

func (r *runner) Press(t *testing.T, bs ...input.Button) {
	t.Helper()
	wait(t, r, r.p.input.init)
	for _, b := range bs {
		deliver(t, r, r.p.input.in, input.Event{
			Button:  b,
			Pressed: true,
		})
	}
}

func testEngraving(t *testing.T, r *runner, scr *EngraveScreen, desc urtypes.OutputDescriptor, mnemonic bip39.Mnemonic, keyIdx int) {
	plateDesc := backup.PlateDesc{
		Descriptor: desc,
		Mnemonic:   mnemonic,
		KeyIdx:     keyIdx,
		Font:       &sh.Fontsh,
	}
	plate, err := backup.Engrave(mjolnir.StrokeWidth, plateDesc)
	if err != nil {
		t.Fatal(err)
	}
	r.p.engrave.closed = make(chan []mjolnir.Cmd, len(plate.Sides))
	for _, side := range plate.Sides {
	done:
		for {
			switch scr.instructions[scr.step].Type {
			case EngraveInstruction:
				break done
			case ConnectInstruction:
				// Hold connect.
				r.Press(t, input.Button3)
				r.p.timeOffset += confirmDelay
			default:
				r.Button(t, input.Button3)
			}
		}
	received:
		for {
			select {
			case got := <-r.p.engrave.closed:
				want := simEngrave(t, side)
				if !reflect.DeepEqual(want, got) {
					t.Fatalf("engraver commands mismatch for side %v", side)
				}
				break received
			default:
				r.Frame(t)
			}
		}
	}
}

func simEngrave(t *testing.T, plate engrave.Command) []mjolnir.Cmd {
	sim := mjolnir.NewSimulator()
	prog := &mjolnir.Program{}
	plate.Engrave(prog)
	prog.Prepare()
	errs := make(chan error, 1)
	go func() {
		errs <- mjolnir.Engrave(sim, prog, nil, nil)
	}()
	plate.Engrave(prog)
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	return sim.Cmds
}

func mnemonicFor(phrase string) bip39.Mnemonic {
	m, err := bip39.ParseMnemonic(phrase)
	if err != nil {
		panic(err)
	}
	return m
}

var twoOfThree = struct {
	Descriptor urtypes.OutputDescriptor
	Mnemonic   bip39.Mnemonic
}{
	Mnemonic: mnemonicFor("flip begin artist fringe online release swift genre wool general transfer arm"),
	Descriptor: urtypes.OutputDescriptor{
		Type:      urtypes.P2WSH,
		Threshold: 2,
		Sorted:    true,
		Keys: []urtypes.KeyDescriptor{
			{
				MasterFingerprint: 0x5a0804e3,
				DerivationPath:    urtypes.Path{0x80000030, 0x80000000, 0x80000000, 0x80000002},
				KeyData:           []byte{0x3, 0xa9, 0x39, 0x4a, 0x2f, 0x1a, 0x4f, 0x99, 0x61, 0x3a, 0x71, 0x69, 0x56, 0xc8, 0x54, 0xf, 0x6d, 0xba, 0x6f, 0x18, 0x93, 0x1c, 0x26, 0x39, 0x10, 0x72, 0x21, 0xb2, 0x67, 0xd7, 0x40, 0xaf, 0x23},
				ChainCode:         []byte{0xdb, 0xe8, 0xc, 0xbb, 0x4e, 0xe, 0x41, 0x8b, 0x6, 0xf4, 0x70, 0xd2, 0xaf, 0xe7, 0xa8, 0xc1, 0x7b, 0xe7, 0x1, 0xab, 0x20, 0x6c, 0x59, 0xa6, 0x5e, 0x65, 0xa8, 0x24, 0x1, 0x6a, 0x6c, 0x70},
				ParentFingerprint: 0xc7bce7a8,
			},
			{
				MasterFingerprint: 0xdd4fadee,
				DerivationPath:    urtypes.Path{0x80000030, 0x80000000, 0x80000000, 0x80000002},
				KeyData:           []byte{0x2, 0x21, 0x96, 0xad, 0xc2, 0x5f, 0xde, 0x16, 0x9f, 0xe9, 0x2e, 0x70, 0x76, 0x90, 0x59, 0x10, 0x22, 0x75, 0xd2, 0xb4, 0xc, 0xc9, 0x87, 0x76, 0xea, 0xab, 0x92, 0xb8, 0x2a, 0x86, 0x13, 0x5e, 0x92},
				ChainCode:         []byte{0x43, 0x8e, 0xff, 0x7b, 0x3b, 0x36, 0xb6, 0xd1, 0x1a, 0x60, 0xa2, 0x2c, 0xcb, 0x93, 0x6, 0xee, 0xa3, 0x5, 0xb0, 0x43, 0x9f, 0x1e, 0xa0, 0x9d, 0x59, 0x28, 0x1, 0x5d, 0xe3, 0x73, 0x81, 0x16},
				ParentFingerprint: 0x22969377,
			},
			{
				MasterFingerprint: 0x9bacd5c0,
				DerivationPath:    urtypes.Path{0x80000030, 0x80000000, 0x80000000, 0x80000002},
				KeyData:           []byte{0x2, 0xfb, 0x72, 0x50, 0x7f, 0xc2, 0xd, 0xdb, 0xa9, 0x29, 0x91, 0xb1, 0x7c, 0x4b, 0xb4, 0x66, 0x13, 0xa, 0xd9, 0x3a, 0x88, 0x6e, 0x73, 0x17, 0x50, 0x33, 0xbb, 0x43, 0xe3, 0xbc, 0x78, 0x5a, 0x6d},
				ChainCode:         []byte{0x95, 0xb3, 0x49, 0x13, 0x93, 0x7f, 0xa5, 0xf1, 0xc6, 0x20, 0x5b, 0x52, 0x5b, 0xb5, 0x7d, 0xe1, 0x51, 0x76, 0x25, 0xe0, 0x45, 0x86, 0xb5, 0x95, 0xbe, 0x68, 0xe7, 0x13, 0x62, 0xd3, 0xed, 0xc5},
				ParentFingerprint: 0x97ec38f9,
			},
		},
	},
}

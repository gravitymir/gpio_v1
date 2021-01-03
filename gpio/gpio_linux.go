package gpio

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

//By default, pins 14 and 15 boot to UART mode, so they are going to be ignored for now.
//We can add them in later as necessary.
//Pins map UPBoard and RaspberyPi? convert to Linux numbers
const (
	PIN_1     = "3.3V"
	PIN_2     = "5.0V"
	PIN_3     = 2
	PIN_4     = "5.0V"
	PIN_5     = 3
	PIN_6     = "Ground"
	PIN_7     = 4
	PIN_8     = 14
	PIN_9     = "Ground"
	PIN_10    = 15
	PIN_11    = 17
	PIN_12    = 18
	PIN_13    = 27
	PIN_14    = "Ground"
	PIN_15    = 22
	PIN_16    = 23
	PIN_17    = "3.3V"
	PIN_18    = 24
	PIN_19    = 10
	PIN_20    = "Ground"
	PIN_21    = 9
	PIN_22    = 25
	PIN_23    = 11
	PIN_24    = 8
	PIN_25    = "Ground"
	PIN_26    = 7
	PIN_27    = 0
	PIN_28    = 1
	PIN_29    = 5
	PIN_30    = "Ground"
	PIN_31    = 6
	PIN_32    = 12
	PIN_33    = 13
	PIN_34    = "Ground"
	PIN_35    = 19
	PIN_36    = 16
	PIN_37    = 26
	PIN_38    = 20
	PIN_39    = "Ground"
	PIN_40    = 21
	GPIOCount = 28

	gpiobase     = "/sys/class/gpio"
	exportPath   = gpiobase + "/export"
	unexportPath = gpiobase + "/unexport"
)

var (
	bytesSet   = []byte{'1'}
	bytesClear = []byte{'0'}
)

// watchEventCallbacks is a map of pins and their callbacks when
// watching for interrupts
var watchEventCallbacks map[int]*pin

// epollFD is the FD for epoll
var epollFD int

func init() {
	setupEpoll()
	watchEventCallbacks = make(map[int]*pin)
}

// setupEpoll sets up epoll for use
func setupEpoll() {
	var err error
	epollFD, err = syscall.EpollCreate1(0)
	if err != nil {
		fmt.Println("Unable to create epoll FD: ", err.Error())
		os.Exit(1)
	}

	go func() {

		var epollEvents [GPIOCount]syscall.EpollEvent

		for {
			numEvents, err := syscall.EpollWait(epollFD, epollEvents[:], -1)
			if err != nil {
				if err == syscall.EINTR || err == syscall.EAGAIN {
					continue
				}
				panic(fmt.Sprintf("EpollWait error: %v", err))
			}
			for i := 0; i < numEvents; i++ {
				if eventPin, exists := watchEventCallbacks[int(epollEvents[i].Fd)]; exists {
					if eventPin.initial {
						eventPin.initial = false
					} else {
						eventPin.callback()
					}
				}
			}
		}

	}()
}

// pin represents a GPIO pin.
type pin struct {
	number        int      // the pin number
	numberAsBytes []byte   // the pin number as a byte array to avoid converting each time
	modePath      string   // the path to the /direction FD to avoid string joining each time
	edgePath      string   // the path to the /edge FD to avoid string joining each time
	valueFile     *os.File // the file handle for the value file
	callback      IRQEvent // the callback function to call when an interrupt occurs
	initial       bool     // is this the initial epoll trigger?
	err           error    //the last error
}

// OpenPin exports the pin, creating the virtual files necessary for interacting with the pin.
// It also sets the mode for the pin, making it ready for use.
func OpenPin(n int, mode Mode) (Pin, error) {
	// export this pin to create the virtual files on the system
	pinBase, err := expose(n)
	if err != nil {
		return nil, err
	}
	value, err := os.OpenFile(filepath.Join(pinBase, "value"), os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	p := &pin{
		number:    n,
		modePath:  filepath.Join(pinBase, "direction"),
		edgePath:  filepath.Join(pinBase, "edge"),
		valueFile: value,
		initial:   true,
	}
	if err := p.setMode(mode); err != nil {
		p.Close()
		return nil, err
	}
	return p, nil
}

// write opens a file for writing, writes the byte slice to it and closes the
// file.
func write(buf []byte, path string) error {
	file, err := os.OpenFile(path, os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err := file.Write(buf); err != nil {
		return err
	}
	return file.Close()
}

// read opens a file for reading, reads the bytes slice from it and closes the file.
func read(path string) ([]byte, error) {
	return ioutil.ReadFile(path)
}

// Close destroys the virtual files on the filesystem, unexporting the pin.
func (p *pin) Close() error {
	return writeFile(filepath.Join(gpiobase, "unexport"), "%d", p.number)
}

// Mode retrieves the current mode of the pin.
func (p *pin) Mode() Mode {
	var mode string
	mode, p.err = readFile(p.modePath)
	return Mode(mode)
}

// SetMode sets the mode of the pin.
func (p *pin) SetMode(mode Mode) {
	p.err = p.setMode(mode)
}

func (p *pin) GetMode() Mode {
	currentMode, _ := read(p.modePath)
	currentMode_ := strings.Trim(string(currentMode), "\n ")
	return Mode(currentMode_)
}

func (p *pin) setMode(mode Mode) error {
	if p.GetMode() != mode {
		return write([]byte(mode), p.modePath)
	} else {
		return nil
	}
}

// Set sets the pin level high.
func (p *pin) Set() {
	_, p.err = p.valueFile.Write(bytesSet)
}

// Clear sets the pin level low.
func (p *pin) Clear() {
	_, p.err = p.valueFile.Write(bytesClear)
}

// Get retrieves the current pin level.
func (p *pin) Get() bool {
	bytes := make([]byte, 1)
	_, p.err = p.valueFile.ReadAt(bytes, 0)
	return bytes[0] == bytesSet[0]
}

// Watch waits for the edge level to be triggered and then calls the callback
// Watch sets the pin mode to input on your behalf, then establishes the interrupt on
// the edge provided

func (p *pin) BeginWatch(edge Edge, callback IRQEvent) error {
	if p.GetMode() != ModeInput {
		fmt.Printf("Error BeginWatch: pin input mode is not \"IN\" %+v", p)
		panic("Error BeginWatch: pin input mode is not correct")
	}
	//p.SetMode(ModeInput)
	if err := write([]byte(edge), p.edgePath); err != nil {
		return err
	}

	var event syscall.EpollEvent
	event.Events = syscall.EPOLLIN | (syscall.EPOLLET & 0xffffffff) | syscall.EPOLLPRI

	fd := int(p.valueFile.Fd())

	p.callback = callback
	watchEventCallbacks[fd] = p

	if err := syscall.SetNonblock(fd, true); err != nil {
		return err
	}

	event.Fd = int32(fd)

	if err := syscall.EpollCtl(epollFD, syscall.EPOLL_CTL_ADD, fd, &event); err != nil {
		return err
	}

	return nil

}

// EndWatch stops watching the pin
func (p *pin) EndWatch() error {

	fd := int(p.valueFile.Fd())

	if err := syscall.EpollCtl(epollFD, syscall.EPOLL_CTL_DEL, fd, nil); err != nil {
		return err
	}

	if err := syscall.SetNonblock(fd, false); err != nil {
		return err
	}

	delete(watchEventCallbacks, fd)

	return nil

}

// Wait blocks while waits for the pin state to match the condition, then returns.
func (p *pin) Wait(condition bool) {
	panic("Wait is not yet implemented!")
}

// Err returns the last error encountered.
func (p *pin) Err() error {
	return p.err
}

func expose(pin int) (string, error) {
	pinBase := filepath.Join(gpiobase, fmt.Sprintf("gpio%d", pin))
	var err error
	if _, statErr := os.Stat(pinBase); os.IsNotExist(statErr) {
		err = writeFile(filepath.Join(gpiobase, "export"), "%d", pin)
	}
	return pinBase, err
}

func writeFile(path string, format string, args ...interface{}) error {
	f, err := os.OpenFile(path, os.O_WRONLY, 0777)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, format, args...)
	return err
}

func readFile(path string) (string, error) {
	buf, err := ioutil.ReadFile(path)
	return strings.TrimSpace(string(buf)), err
}

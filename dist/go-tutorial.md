# 🐹 The Complete Go Language Tutorial

A hands-on introduction to Go (Golang) — from your first program to concurrency.

> **Audience:** Beginners with basic programming knowledge (variables, functions, loops).
> **Go version used:** 1.22+ (works with most modern versions)

---

## Table of Contents

1. [What is Go?](#1-what-is-go)
2. [Installation & Setup](#2-installation--setup)
3. [Hello, World](#3-hello-world)
4. [Variables & Basic Types](#4-variables--basic-types)
5. [Constants](#5-constants)
6. [Operators](#6-operators)
7. [Control Flow](#7-control-flow)
8. [Functions](#8-functions)
9. [Arrays, Slices & Maps](#9-arrays-slices--maps)
10. [Structs & Methods](#10-structs--methods)
11. [Interfaces](#11-interfaces)
12. [Error Handling](#12-error-handling)
13. [Pointers](#13-pointers)
14. [Concurrency: Goroutines & Channels](#14-concurrency-goroutines--channels)
15. [Packages & Modules](#15-packages--modules)
16. [Testing](#16-testing)
17. [Useful Standard Library](#17-useful-standard-library)
18. [Next Steps](#18-next-steps)

---

## 1. What is Go?

Go is an open-source programming language created at **Google** in 2009 by
Robert Griesemer, Rob Pike, and Ken Thompson.

**Key features:**

| Feature | Description |
|---|---|
| ⚡ Fast compilation | Compiles to a single native binary in seconds |
| 🧵 Built-in concurrency | Goroutines and channels make parallelism easy |
| 🗑️ Garbage collected | Automatic memory management, low-latency GC |
| 🧼 Simple syntax | ~25 keywords; the language is easy to read |
| 📦 Static typing | Catch bugs at compile time, great tooling |
| 🚀 Great for servers | Web services, CLIs, cloud infrastructure (Docker & Kubernetes are written in Go!) |

**What Go is good for:** web backends, APIs, CLI tools, DevOps tooling, networking, distributed systems.

---

## 2. Installation & Setup

### 2.1 Install Go

Download from https://go.dev/dl/ or use a package manager:

```bash
# macOS (Homebrew)
brew install go

# Ubuntu/Debian
sudo apt install golang-go

# Windows (Chocolatey)
choco install golang
```

Verify the installation:

```bash
go version
# go version go1.22.x darwin/amd64
```

### 2.2 Set up your editor

The recommended editor is **VS Code** with the official **Go extension**
(Installs `gopls` for auto-completion, `go vet` for linting, and more).
GoLand (JetBrains) is a popular alternative.

### 2.3 Hello toolchain

```bash
go help        # list all commands
go fmt ./...   # format code (do this constantly!)
go run main.go # compile & run
go build       # produce a binary
go test ./...  # run tests
```

---

## 3. Hello, World

Create a project folder and a file `main.go`:

```go
package main

import "fmt"

func main() {
	fmt.Println("Hello, World!")
}
```

Run it:

```bash
go run main.go
# Hello, World!
```

**Line by line:**

- `package main` — every Go file belongs to a package. `main` is special: it defines a standalone executable.
- `import "fmt"` — import the `fmt` package (formatted I/O).
- `func main()` — the entry point. Execution starts here.
- `fmt.Println(...)` — print a line to stdout.

⚠️ **Note:** Go uses `go fmt`-style formatting — **tabs** for indentation, and braces `{` must be on the same line:

```go
// ❌ This does NOT compile:
func main()
{
}
```

---

## 4. Variables & Basic Types

### 4.1 Declaring variables

```go
package main

import "fmt"

func main() {
	// 1. Declare with an initial value (type is inferred)
	name := "Alice"          // string
	age := 30                // int

	// 2. Explicit declaration
	var score float64 = 92.5

	// 3. Declare without a value (gets "zero value")
	var active bool          // false
	var count int            // 0
	var label string         // ""

	fmt.Println(name, age, score, active, count, label)
}
```

**Rules to remember:**

- `:=` declares **and** assigns (inside functions only).
- `var x T` declares with a type; unused *local* variables are a **compile error**.
- Types are inferred but static — `name := "hi"; name = 42` won't compile.

### 4.2 Basic types

| Category | Types | Notes |
|---|---|---|
| Integers (signed) | `int`, `int8`, `int16`, `int32`, `int64` | `int` is 64-bit on modern platforms |
| Integers (unsigned) | `uint`, `uint8`, `uint16`, `uint32`, `uint64` | `uint8` == `byte` |
| Floats | `float32`, `float64` | Prefer `float64` |
| Complex | `complex64`, `complex128` | Rarely needed |
| Text | `string` | Immutable UTF-8 |
| Boolean | `bool` | `true` / `false` |
| Special | `rune` (int32 alias), `byte` (uint8 alias) | `rune` = a Unicode code point |

### 4.3 Strings

```go
package main

import (
	"fmt"
	"strings"
)

func main() {
	s := "Hello, Go"

	// Length (in bytes!)
	fmt.Println(len(s)) // 9

	// Concatenation
	greeting := "Hi, " + "there"
	fmt.Println(greeting)

	// Slicing & common methods
	fmt.Println(strings.ToUpper(s))        // HELLO, GO
	fmt.Println(strings.Contains(s, "Go")) // true
	fmt.Println(strings.Split(s, ", "))    // [Hello Go]

	// Multi-line strings use backticks
	poem := `Roses are red,
	Violets are blue.`
	fmt.Println(poem)

	// Formatted strings
	msg := fmt.Sprintf("Name: %s, Age: %d", "Alice", 30)
	fmt.Println(msg)
}
```

**Common format verbs:**

| Verb | Meaning |
|---|---|
| `%s` | string |
| `%d` | integer |
| `%f` | float (default width) |
| `%.2f` | float, 2 decimals |
| `%t` | boolean |
| `%v` | any value (default format) |
| `%+v` | struct with field names |
| `%T` | type of the value |

### 4.4 Type conversion

Go has **no implicit conversions** — you must convert explicitly:

```go
i := 42
f := float64(i)  // 42.0
u := uint8(i)    // 42
s := strconv.Itoa(i) // "42"

f2 := 3.14
i2 := int(f2)    // 3 (truncates!)
```

---

## 5. Constants

Constants are declared with `const` and must be known at compile time.

```go
package main

import "fmt"

const Pi = 3.14159
const Greeting = "Hello"

// Grouped constants
const (
	StatusOK    = 200
	StatusFound = 302
	StatusError = 500
)

// iota auto-increments — great for enumerations
type Weekday int

const (
	Sunday Weekday = iota // 0
	Monday                // 1
	Tuesday               // 2
	Wednesday             // 3
	Thursday              // 4
	Friday                // 5
	Saturday              // 6
)

func main() {
	fmt.Println(Pi, StatusOK, Monday)
	fmt.Println(Saturday) // 6
}
```

---

## 6. Operators

```go
// Arithmetic
a + b   a - b   a * b   a / b   a % b
// Note: int/int performs integer division: 7/2 == 3

// Comparison
a == b   a != b   a < b   a > b   a <= b   a >= b

// Logical
&&   ||   !

// Bitwise
&   |   ^   &^ (AND NOT)   <<   >>
```

`++` and `--` exist but are **statements only** (no `x = i++`), and there is **no ternary operator** (`a ? b : c`) — use `if/else`.

---

## 7. Control Flow

### 7.1 if / else

```go
age := 20

if age >= 18 {
	fmt.Println("Adult")
} else if age >= 13 {
	fmt.Println("Teenager")
} else {
	fmt.Println("Child")
}

// "if" with a short statement (variable scoped to the if/else block)
if n := len("hello"); n > 3 {
	fmt.Println("Long word:", n)
}
```

Conditions do **not** need parentheses; braces are **required**.

### 7.2 for — Go's only loop

Go has exactly one loop keyword: `for`. It covers everything.

```go
package main

import "fmt"

func main() {
	// 1. Classic C-style
	for i := 0; i < 5; i++ {
		fmt.Print(i, " ")
	}
	// Output: 0 1 2 3 4

	// 2. "while" style
	count := 0
	for count < 3 {
		fmt.Print(count, " ")
		count++
	}
	// Output: 0 1 2

	// 3. Infinite loop (break to exit)
	x := 0
	for {
		x++
		if x >= 3 {
			break
		}
	}

	// 4. Loop over a collection with range
	fruits := []string{"apple", "banana", "cherry"}
	for index, value := range fruits {
		fmt.Println(index, value)
	}

	// Skip elements with continue
	for i := 0; i < 10; i++ {
		if i%2 == 0 {
			continue // skip even numbers
		}
		fmt.Print(i, " ") // 1 3 5 7 9
	}
}
```

### 7.3 switch

`switch` in Go is cleaner than C — no `break` needed (cases don't fall through).

```go
day := "Sat"

switch day {
case "Sat", "Sun":
	fmt.Println("Weekend 🎉")
case "Mon":
	fmt.Println("Back to work")
default:
	fmt.Println("Regular day")
}

// Switch without an expression — acts like if/else chain
hour := 14
switch {
case hour < 12:
	fmt.Println("Good morning")
case hour < 18:
	fmt.Println("Good afternoon")
default:
	fmt.Println("Good evening")
}

// Type switch (used a lot with interfaces)
func describe(i interface{}) {
	switch v := i.(type) {
	case int:
		fmt.Println("int:", v)
	case string:
		fmt.Println("string:", v)
	default:
		fmt.Println("unknown type")
	}
}
```

---

## 8. Functions

### 8.1 Basics

```go
package main

import "fmt"

// Parameters and a single return value
func add(a int, b int) int {
	return a + b
}

// Multiple return values (very common in Go!)
func divide(a, b float64) (float64, error) {
	if b == 0 {
		return 0, fmt.Errorf("cannot divide by zero")
	}
	return a / b, nil
}

// Named return values
func split(sum int) (x, y int) {
	x = sum * 4 / 9
	y = sum - x
	return // "naked" return — returns x and y
}

func main() {
	fmt.Println(add(2, 3)) // 5

	result, err := divide(10, 4)
	if err != nil {
		fmt.Println("Error:", err)
		return
	}
	fmt.Println(result) // 2.5

	// Ignore a value with _
	_, err = divide(1, 0)
	fmt.Println(err) // cannot divide by zero
}
```

### 8.2 Variadic functions

```go
func sum(nums ...int) int {
	total := 0
	for _, n := range nums {
		total += n
	}
	return total
}

// sum(1, 2, 3)  -> 6
// sum()         -> 0

// Spread a slice into a variadic function:
numbers := []int{4, 5, 6}
sum(numbers...) // 15
```

### 8.3 Closures (functions as values)

Functions are first-class citizens in Go:

```go
func main() {
	// Assign a function to a variable
	double := func(x int) int {
		return x * 2
	}
	fmt.Println(double(5)) // 10

	// Closure: captures outer variables
	counter := 0
	increment := func() {
		counter++ // captures `counter` by reference
	}
	increment()
	increment()
	fmt.Println(counter) // 2

	// Function that returns a function
	makeGreeter := func(name string) func() string {
		return func() string {
			return "Hello, " + name
		}
	}
	hi := makeGreeter("Alice")
	fmt.Println(hi()) // Hello, Alice
}
```

### 8.4 defer

`defer` schedules a function call to run when the enclosing function returns —
perfect for cleanup (closing files, unlocking mutexes).

```go
func readFile(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close() // runs right before readFile returns, even on panic

	// ... use f ...
}
```

Deferred calls run in **LIFO order** (last deferred, first executed).

---

## 9. Arrays, Slices & Maps

### 9.1 Arrays (fixed size — rarely used directly)

```go
var arr [3]int              // [0 0 0]
nums := [3]int{1, 2, 3}     // [1 2 3]
nums[0] = 10

// Array size is part of the type: [3]int != [4]int
// Arrays are copied when assigned/passed — this is why slices exist.
```

### 9.2 Slices (dynamic — use these!)

A slice is a view into an underlying array: it has a **length** and a **capacity**.

```go
package main

import "fmt"

func main() {
	// Create slices
	s := []int{1, 2, 3}                 // literal
	empty := make([]int, 3)             // len=3, cap=3, all zeros
	buffer := make([]int, 0, 10)        // len=0, cap=10 (pre-allocated)

	// Append (may reallocate — always reassign!)
	s = append(s, 4, 5)     // [1 2 3 4 5]
	s = append(s, []int{6, 7}...) // append another slice

	// Length & capacity
	fmt.Println(len(s), cap(s))

	// Slicing [start:end] — end is exclusive
	fmt.Println(s[1:3])  // [2 3]
	fmt.Println(s[:2])   // [1 2]
	fmt.Println(s[2:])   // [3 4 5 6 7]

	// Copy
	dst := make([]int, len(s))
	copy(dst, s)

	// Iterating
	for i, v := range s {
		fmt.Println(i, v)
	}
}
```

**Gotcha — slices share memory:**

```go
a := []int{1, 2, 3, 4, 5}
b := a[1:3]      // [2 3] — shares the same underlying array
b[0] = 99
fmt.Println(a)   // [1 99 3 4 5] 😱
// Use slices.Clone (Go 1.21+) for an independent copy
```

### 9.3 Maps (key → value)

```go
package main

import "fmt"

func main() {
	// Create
	ages := map[string]int{
		"Alice": 30,
		"Bob":   25,
	}
	scores := make(map[string]float64)

	// Set / get
	ages["Carol"] = 28
	fmt.Println(ages["Alice"]) // 30

	// Missing keys return the zero value
	fmt.Println(ages["Nobody"]) // 0 (not an error!)

	// The "comma ok" idiom to check existence
	age, ok := ages["Bob"]
	if ok {
		fmt.Println("Bob is", age)
	}

	// Delete
	delete(ages, "Bob")

	// Iterate (order is RANDOM — don't rely on it!)
	for name, age := range ages {
		fmt.Println(name, age)
	}
}
```

### 9.4 Quick reference

| Need | Use |
|---|---|
| Ordered list of items | slice `[]T` |
| Lookup by key | map `map[K]V` |
| True/false per item | map with `bool` values (poor man's set) |
| Fixed-size data | array `[N]T` (rare) |

---

## 10. Structs & Methods

Structs are Go's way to group related data (there are no classes).

### 10.1 Defining and using structs

```go
package main

import "fmt"

type User struct {
	Name  string
	Email string
	Age   int
}

func main() {
	// Create with field names (recommended)
	alice := User{Name: "Alice", Email: "alice@example.com", Age: 30}

	// Positional (all fields required, in order)
	bob := User{"Bob", "bob@example.com", 25}

	// Pointer to struct
	carol := &User{Name: "Carol", Age: 28}

	fmt.Println(alice)        // {Alice alice@example.com 30}
	fmt.Printf("%+v\n", bob)  // {Name:Bob Email:bob@example.com Age:25}
	fmt.Println(carol.Name)   // Carol (auto-dereferences: no -> needed)
}
```

### 10.2 Methods

Methods are functions with a **receiver**.

```go
// Value receiver: gets a COPY of the struct
func (u User) Greet() string {
	return "Hi, I'm " + u.Name
}

// Pointer receiver: can modify the struct (and avoids copying)
func (u *User) HaveBirthday() {
	u.Age++
}

func main() {
	u := User{Name: "Alice", Age: 30}
	fmt.Println(u.Greet()) // Hi, I'm Alice

	u.HaveBirthday() // Go auto-takes the address: (&u).HaveBirthday()
	fmt.Println(u.Age) // 31
}
```

**Rule of thumb:** use pointer receivers if the method **modifies** the receiver,
or if the struct is large. Be **consistent** within a type.

### 10.3 Embedding (composition over inheritance)

Go has no inheritance — it favors composition:

```go
type Animal struct {
	Name string
}

func (a Animal) Speak() string {
	return a.Name + " makes a sound"
}

type Dog struct {
	Animal // embedded — Dog "has an" Animal
	Breed  string
}

func main() {
	d := Dog{Animal{Name: "Rex"}, "Labrador"}
	fmt.Println(d.Speak())       // Rex makes a sound (promoted method!)
	fmt.Println(d.Name)          // Rex (promoted field)
}
```

---

## 11. Interfaces

Interfaces define *behavior*: a set of method signatures.
Types satisfy interfaces **implicitly** — no `implements` keyword!

```go
package main

import "fmt"

// Interface: anything that can Speak and Move
type Speaker interface {
	Speak() string
}

// --- Implementations ---

type Dog struct{ Name string }
func (d Dog) Speak() string { return d.Name + ": Woof!" }

type Cat struct{ Name string }
func (c Cat) Speak() string { return c.Name + ": Meow" }

type Robot struct{ ID int }
func (r Robot) Speak() string { return fmt.Sprintf("Unit-%d: BEEP", r.ID) }

// --- Polymorphism: works with ANY Speaker ---

func makeAllSpeak(speakers []Speaker) {
	for _, s := range speakers {
		fmt.Println(s.Speak())
	}
}

func main() {
	zoo := []Speaker{
		Dog{Name: "Rex"},
		Cat{Name: "Whiskers"},
		Robot{ID: 42},
	}
	makeAllSpeak(zoo)
	// Rex: Woof!
	// Whiskers: Meow
	// Unit-42: BEEP
}
```

**Key interface facts:**

- The **empty interface** `interface{}` (or `any`, Go 1.18+) matches every type.
- Small interfaces are idiomatic: `fmt.Stringer` (`String() string`), `error` (`Error() string`), `io.Reader`, `io.Writer`.
- Interfaces are satisfied implicitly — a type satisfies an interface if it has all the methods.

**Generics (Go 1.18+)** let you write type-safe reusable code:

```go
func Map[T, U any](s []T, f func(T) U) []U {
	out := make([]U, 0, len(s))
	for _, v := range s {
		out = append(out, f(v))
	}
	return out
}

nums := []int{1, 2, 3}
doubled := Map(nums, func(n int) int { return n * 2 }) // [2 4 6]
```

---

## 12. Error Handling

Go uses **explicit error values** instead of exceptions.

```go
package main

import (
	"errors"
	"fmt"
)

// Sentinel error
var ErrNotFound = errors.New("item not found")

func findItem(id int) (string, error) {
	if id != 42 {
		return "", ErrNotFound
	}
	return "the answer", nil
}

func main() {
	item, err := findItem(7)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			fmt.Println("Not found — try id 42")
			return
		}
		fmt.Println("Unexpected error:", err)
		return
	}
	fmt.Println("Found:", item)
}
```

### 12.2 Wrapping errors

Use `%w` to add context while preserving the original error:

```go
func loadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("load config: %w", err)
	}
	// ...
}

// Later, unwrap to inspect the cause:
if err != nil {
	if errors.Is(err, os.ErrNotExist) { ... }  // checks wrapped chain
	var pathErr *os.PathError
	if errors.As(err, &pathErr) { ... }        // extracts a specific type
}
```

### 12.3 panic & recover

`panic` is for unrecoverable situations (programming bugs). Not for normal errors.

```go
func safeDivide(a, b int) (result int) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Println("Recovered from:", r)
			result = 0
		}
	}()
	return a / b // panics if b == 0
}
```

---

## 13. Pointers

Go has pointers, but no pointer arithmetic.

```go
package main

import "fmt"

func main() {
	x := 42
	p := &x        // p holds the address of x

	fmt.Println(p)  // e.g. 0xc000014098
	fmt.Println(*p) // 42 (dereference)

	*p = 100        // modify x through the pointer
	fmt.Println(x)  // 100
}
```

**Why it matters — function arguments are copies:**

```go
func failToModify(n int)      { n = 99 }  // modifies a copy
func modify(n *int)           { *n = 99 } // modifies the original

type User struct{ Name string }

func rename(u *User, name string) { u.Name = name } // typical usage

func main() {
	x := 1
	failToModify(x)
	fmt.Println(x) // 1
	modify(&x)
	fmt.Println(x) // 99

	u := &User{Name: "old"}
	rename(u, "new")
	fmt.Println(u.Name) // new
}
```

💡 `new(T)` allocates a zeroed `*T`; `make` is for slices, maps, and channels only.

---

## 14. Concurrency: Goroutines & Channels

Go's superpower. A **goroutine** is a lightweight thread managed by the Go runtime
(thousands can run concurrently). Start one with `go`.

### 14.1 Goroutines & sync.WaitGroup

```go
package main

import (
	"fmt"
	"sync"
)

func worker(id int, wg *sync.WaitGroup) {
	defer wg.Done() // tell the WaitGroup we're finished
	fmt.Printf("Worker %d done\n", id)
}

func main() {
	var wg sync.WaitGroup

	for i := 1; i <= 5; i++ {
		wg.Add(1)
		go worker(i, &wg) // launch concurrently!
	}

	wg.Wait() // block until all workers finish
	fmt.Println("All done")
}
```

### 14.2 Channels — communication between goroutines

> "Don't communicate by sharing memory; share memory by communicating."

```go
package main

import "fmt"

func main() {
	// Unbuffered channel: send BLOCKS until someone receives
	ch := make(chan string)

	go func() {
		ch <- "hello" // send
	}()

	msg := <-ch // receive
	fmt.Println(msg) // hello
}
```

**Buffered channels** hold values without a receiver:

```go
ch := make(chan int, 3) // buffer of 3
ch <- 1
ch <- 2
fmt.Println(<-ch) // 1
```

### 14.3 select — wait on multiple channels

```go
func main() {
	c1 := make(chan string)
	c2 := make(chan string)

	go func() { c1 <- "from c1" }()
	go func() { c2 <- "from c2" }()

	for i := 0; i < 2; i++ {
		select {
		case msg := <-c1:
			fmt.Println(msg)
		case msg := <-c2:
			fmt.Println(msg)
		}
	}
	// Prints both messages in random arrival order
}
```

### 14.4 A realistic pipeline

```go
// Producer → worker pool → results
package main

import (
	"fmt"
	"sync"
)

func producer(jobs chan<- int) {
	for i := 1; i <= 10; i++ {
		jobs <- i
	}
	close(jobs) // signals no more work
}

func worker(id int, jobs <-chan int, results chan<- int, wg *sync.WaitGroup) {
	defer wg.Done()
	for j := range jobs { // loop ends when channel is closed
		results <- j * 2
	}
}

func main() {
	jobs := make(chan int)
	results := make(chan int)

	var wg sync.WaitGroup
	for w := 1; w <= 3; w++ {
		wg.Add(1)
		go worker(w, jobs, results, &wg)
	}

	go producer(jobs)

	go func() {
		wg.Wait()
		close(results)
	}()

	for r := range results {
		fmt.Println(r)
	}
}
```

**Channel directions:** `chan<- int` (send-only), `<-chan int` (receive-only) — great for documenting intent.

⚠️ **Avoid data races:** never share a variable between goroutines without a channel or a `sync.Mutex`. Run `go run -race main.go` to detect races.

---

## 15. Packages & Modules

### 15.1 Modules

Every Go project is a *module*:

```bash
mkdir myapp && cd myapp
go mod init github.com/yourname/myapp
# creates go.mod
```

`go.mod`:

```
module github.com/yourname/myapp

go 1.22

require github.com/google/uuid v1.6.0
```

### 15.2 Adding dependencies

```bash
go get github.com/google/uuid   # add dependency
go mod tidy                     # clean up unused deps
```

### 15.3 Project layout

```
myapp/
├── go.mod
├── main.go          # package main
├── internal/        # private code (not importable by others)
│   └── auth/
│       └── auth.go  # package auth
└── cmd/             # multiple binaries (optional)
    └── server/
        └── main.go
```

### 15.4 Writing and using a package

```go
// internal/math/math.go
package math

// Exported names start with a CAPITAL letter.
func Add(a, b int) int { return a + b }

// Unexported (private to this package):
func hidden() {}
```

```go
// main.go
package main

import (
	"fmt"
	"myapp/internal/math" // module path + directory
)

func main() {
	fmt.Println(math.Add(2, 3)) // 5
}
```

> 📌 **Capital letter = public.** Lowercase = package-private. That's Go's entire visibility system.

---

## 16. Testing

Testing is built into the toolchain. Test files end with `_test.go`.

```go
// math.go
package math

func Add(a, b int) int { return a + b }
```

```go
// math_test.go
package math

import "testing"

func TestAdd(t *testing.T) {
	got := Add(2, 3)
	want := 5
	if got != want {
		t.Errorf("Add(2, 3) = %d; want %d", got, want)
	}
}

// Table-driven tests — the idiomatic Go pattern
func TestAddTable(t *testing.T) {
	tests := []struct {
		name string
		a, b int
		want int
	}{
		{"positive", 2, 3, 5},
		{"negatives", -1, -1, -2},
		{"zero", 0, 5, 5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Add(tt.a, tt.b); got != tt.want {
				t.Errorf("got %d, want %d", got, tt.want)
			}
		})
	}
}
```

```bash
go test ./...     # run all tests
go test -v        # verbose
go test -cover    # coverage
go test -bench=.  # benchmarks
```

---

## 17. Useful Standard Library

Go's stdlib is famously complete. A quick tour:

| Package | What it does |
|---|---|
| `fmt` | Formatted I/O |
| `strings`, `strconv` | String manipulation & conversion |
| `os`, `io`, `bufio` | Files, streams, buffered I/O |
| `net/http` | **Web servers & HTTP clients** |
| `encoding/json` | JSON encode/decode |
| `time` | Dates, durations, timers |
| `errors` | Error creation/inspection |
| `sort`, `slices`, `maps` | Sorting & collection helpers |
| `context` | Cancellation & deadlines across APIs |
| `log` / `log/slog` | Logging |

### 17.1 A real web server in ~15 lines

```go
package main

import (
	"encoding/json"
	"log"
	"net/http"
)

func helloHandler(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		name = "world"
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"message": "Hello, " + name})
}

func main() {
	http.HandleFunc("/hello", helloHandler)
	log.Println("Listening on :8080...")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
```

```bash
go run main.go
curl "http://localhost:8080/hello?name=Alice"
# {"message":"Hello, Alice"}
```

### 17.2 JSON

```go
type Person struct {
	Name string `json:"name"`  // struct tags map JSON keys
	Age  int    `json:"age"`
}

// Encode
p := Person{Name: "Alice", Age: 30}
data, _ := json.Marshal(p)          // {"name":"Alice","age":30}

// Decode
var out Person
json.Unmarshal([]byte(`{"name":"Bob","age":25}`), &out)
```

---

## 18. Next Steps

### Practice ideas (in increasing difficulty)

1. **CLI tool** — a to-do list app that saves to a JSON file (`os`, `encoding/json`, `flag`).
2. **Web scraper** — fetch a page and extract links (`net/http`, `strings`/regexp).
3. **REST API** — CRUD endpoints for a "notes" service with in-memory storage (`net/http`).
4. **Concurrent crawler** — crawl N pages in parallel with goroutines + a worker pool.
5. **Chat server** — WebSockets, broadcasting messages between clients.

### Recommended resources

- 📖 **A Tour of Go** — interactive, official: https://go.dev/tour/
- 📖 **Go by Example** — snippet-driven: https://gobyexample.com/
- 📖 **Effective Go** — idioms & style: https://go.dev/doc/effective_go
- 📖 **The Go Programming Language** (Donovan & Kernighan) — the definitive book
- 🛠️ **Standard library docs**: https://pkg.go.dev/std
- 🏗️ **Project layout guide**: https://github.com/golang-standards/project-layout

### Idiomatic Go checklist

- ✅ Run `go fmt` on everything (non-negotiable).
- ✅ Handle every error; don't ignore `err` with `_` unless you have a reason.
- ✅ Keep interfaces small (1–2 methods); define them at the *consumer* side.
- ✅ Prefer composition over inheritance; prefer explicit over clever.
- ✅ Accept interfaces, return concrete types.
- ✅ Use `context.Context` for cancellation in any long-running or networked code.
- ✅ Table-driven tests.

```go
// The Go proverb you'll hear most often:
// "Clear is better than clever."
```

Happy coding! 🚀

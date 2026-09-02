// Channels in Go — a hands-on tutorial program.
//
// A channel is a typed conduit through which goroutines communicate and
// synchronize. Think of it as a pipe: one goroutine puts data in one end,
// another takes it out of the other end.
//
// Run this file with:  go run channels.go
package main

import (
	"fmt"
	"time"
)

// -----------------------------------------------------------------------------
// 1. UNBUFFERED CHANNELS — the basics
// -----------------------------------------------------------------------------
// ch := make(chan string) creates an unbuffered channel.
//
// Key rule: a send blocks until someone receives, and a receive blocks until
// someone sends. This is what makes channels a synchronization tool, not just
// a message box.
func basicChannel() {
	ch := make(chan string) // unbuffered channel of strings

	go func() {
		fmt.Println("  [worker] doing some work...")
		time.Sleep(100 * time.Millisecond) // simulate work
		ch <- "hello from the worker!"     // SEND — blocks until main receives
	}()

	fmt.Println("  [main] waiting for the message...")
	msg := <-ch // RECEIVE — blocks until the worker sends
	fmt.Println("  [main] received:", msg)
}

// -----------------------------------------------------------------------------
// 2. BUFFERED CHANNELS — a queue with capacity
// -----------------------------------------------------------------------------
// make(chan int, 3) creates a channel that can hold up to 3 values.
// A send only blocks when the buffer is FULL; a receive blocks when it's EMPTY.
func bufferedChannel() {
	ch := make(chan int, 3)

	ch <- 1 // buffer: [1]        — no receiver needed yet
	ch <- 2 // buffer: [1, 2]
	ch <- 3 // buffer: [1, 2, 3]  — full now
	// ch <- 4 // would block here until someone receives!

	fmt.Println("  [main] buffered channel holds:", <-ch, <-ch, <-ch)
}

// -----------------------------------------------------------------------------
// 3. CLOSING CHANNELS & ranging over them
// -----------------------------------------------------------------------------
// close(ch) signals "no more values will be sent".
// Receiving from a closed channel returns the zero value immediately.
// The `for v := range ch` loop reads until the channel is closed —
// a very common way to consume a stream of values.
func closingAndRange() {
	jobs := make(chan int, 5)

	go func() {
		for i := 1; i <= 5; i++ {
			jobs <- i * i // send squares of 1..5
		}
		close(jobs) // "that's all folks!" — receivers can finish
	}()

	// range over a channel: receives until it's closed.
	for v := range jobs {
		fmt.Println("  [main] got:", v)
	}

	// The comma-ok idiom tells you whether a channel is closed:
	v, ok := <-jobs
	fmt.Printf("  [main] after close: value=%d, ok=%v (false means closed & empty)\n", v, ok)
}

// -----------------------------------------------------------------------------
// 4. SELECT — waiting on multiple channels at once
// -----------------------------------------------------------------------------
// select is like a switch, but for channel operations. It blocks until one of
// its cases can proceed; if several are ready, it picks one at random.
// The `default` case makes it non-blocking.
func selectStatement() {
	ch1 := make(chan string)
	ch2 := make(chan string)

	go func() {
		time.Sleep(50 * time.Millisecond)
		ch1 <- "from channel 1"
	}()
	go func() {
		time.Sleep(100 * time.Millisecond)
		ch2 <- "from channel 2"
	}()

	// Receive from whichever channel is ready first.
	for i := 0; i < 2; i++ {
		select {
		case msg := <-ch1:
			fmt.Println("  [main] select got:", msg)
		case msg := <-ch2:
			fmt.Println("  [main] select got:", msg)
		}
	}

	// Non-blocking send/receive with default:
	select {
	case msg := <-ch1:
		fmt.Println("  [main] got:", msg)
	default:
		fmt.Println("  [main] nothing ready right now — moving on!")
	}

	// select with time.After is the classic timeout pattern:
	select {
	case msg := <-ch1:
		fmt.Println("  [main] got:", msg)
	case <-time.After(50 * time.Millisecond):
		fmt.Println("  [main] timed out waiting!")
	}
}

// -----------------------------------------------------------------------------
// 5. DIRECTIONAL CHANNELS — restricting send vs receive
// -----------------------------------------------------------------------------
// You can restrict how a function uses a channel:
//   chan<- T  — send-only
//   <-chan T  — receive-only
// This documents intent and lets the compiler catch mistakes.
func producer(out chan<- int) { // out can ONLY be sent to
	for i := 1; i <= 3; i++ {
		out <- i
	}
	close(out)
}

func consumer(in <-chan int, done chan<- bool) { // in can ONLY be received from
	sum := 0
	for v := range in {
		sum += v
	}
	fmt.Println("  [consumer] sum of values:", sum)
	done <- true
}

func directionalChannels() {
	numbers := make(chan int)
	done := make(chan bool)

	go producer(numbers)
	go consumer(numbers, done)

	<-done // wait for the consumer to finish
}

// -----------------------------------------------------------------------------
// 6. WORKER POOL — a classic pattern combining everything above
// -----------------------------------------------------------------------------
// N workers pull tasks from a shared `jobs` channel and push results into a
// `results` channel. This scales processing across goroutines safely.
func workerPool() {
	const numWorkers = 3

	jobs := make(chan int, 10)
	results := make(chan string, 10)

	// Start the workers.
	for w := 1; w <= numWorkers; w++ {
		go func(id int) {
			for job := range jobs { // workers share the jobs channel;
				time.Sleep(20 * time.Millisecond) // each task is taken by ONE worker
				results <- fmt.Sprintf("worker %d processed job %d", id, job)
			}
		}(w)
	}

	// Feed 6 jobs.
	for j := 1; j <= 6; j++ {
		jobs <- j
	}
	close(jobs) // tell workers: no more jobs

	// Collect results (sync.WaitGroup would be the idiomatic choice,
	// but a counting loop keeps this example channel-only).
	for i := 0; i < 6; i++ {
		fmt.Println("  [main]", <-results)
	}
}

// -----------------------------------------------------------------------------

func main() {
	fmt.Println("1) Basic unbuffered channel:")
	basicChannel()

	fmt.Println("\n2) Buffered channel:")
	bufferedChannel()

	fmt.Println("\n3) Closing channels & range:")
	closingAndRange()

	fmt.Println("\n4) select statement:")
	selectStatement()

	fmt.Println("\n5) Directional channels:")
	directionalChannels()

	fmt.Println("\n6) Worker pool:")
	workerPool()

	fmt.Println("\nDone!")
}

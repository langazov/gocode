package main

import "fmt"

func main() {
	// ---------- 1. Basic unbuffered channel: send blocks until receive ----------
	ch := make(chan string)
	go func() {
		ch <- "hello" // blocks until main receives
	}()
	fmt.Println("1:", <-ch)

	// ---------- 2. Buffered channel: send doesn't block until full ----------
	buf := make(chan int, 3)
	buf <- 1
	buf <- 2
	buf <- 3
	// buf <- 4 would block here (buffer full)
	fmt.Println("2:", <-buf, <-buf, <-buf)

	// ---------- 3. Channel direction: producer-only / consumer-only ----------
	results := make(chan int, 2)
	go produce(results)
	fmt.Println("3:", <-results, <-results)

	// ---------- 4. range over a channel until it's closed ----------
	jobs := make(chan int, 5)
	for i := 1; i <= 5; i++ {
		jobs <- i
	}
	close(jobs) // closing lets range terminate
	sum := 0
	for v := range jobs {
		sum += v
	}
	fmt.Println("4: sum =", sum)

	// ---------- 5. select: wait on multiple channel operations ----------
	a, b := make(chan string), make(chan string)
	go func() { a <- "from a" }()
	go func() { b <- "from b" }()
	fmt.Println("5:", <-a, "then", <-b)

	// select with default = non-blocking check
	empty := make(chan int)
	select {
	case <-empty:
		fmt.Println("5: got value")
	default:
		fmt.Println("5: nothing ready (non-blocking)")
	}

	// ---------- 6. Worker pool (the classic pattern) ----------
	const workers, tasks = 3, 6
	taskCh := make(chan int)
	doneCh := make(chan int, tasks)
	for w := 1; w <= workers; w++ {
		go func(id int) {
			for t := range taskCh { // workers pull from shared channel
				doneCh <- id*100 + t
			}
		}(w)
	}
	for t := 1; t <= tasks; t++ {
		taskCh <- t
	}
	close(taskCh)
	out := []int{}
	for i := 0; i < tasks; i++ {
		out = append(out, <-doneCh)
	}
	fmt.Println("6:", out)

	// ---------- 7. done channel for cancellation ----------
	cancel := make(chan struct{})
	go func() {
		<-cancel // receive from closed channel never blocks
		fmt.Println("7: worker cancelled cleanly")
	}()
	close(cancel) // broadcast: all receivers of a closed channel proceed

	// ---------- 8. receive with ok: detect closed channel ----------
	closed := make(chan int, 1)
	closed <- 42
	close(closed)
	v, ok := <-closed
	fmt.Printf("8: v=%d ok=%t\n", v, ok)
	v, ok = <-closed
	fmt.Printf("8: v=%d ok=%t\n", v, ok) // zero value after drained
}

func produce(out chan<- int) { // send-only inside this func
	out <- 10
	out <- 20
}

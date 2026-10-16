package main

type Greeter interface {
	Greet() string
}

type English struct{}

func (English) Greet() string { return "hello" }

type French struct{}

func (French) Greet() string { return "bonjour" }

func say(g Greeter) string {
	return g.Greet()
}

func main() {
	var g Greeter = English{}
	go func() {
		say(g)
	}()
	println(say(French{}))
}

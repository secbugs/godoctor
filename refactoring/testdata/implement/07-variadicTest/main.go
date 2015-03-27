package main // <<<<< stubInterface,1,1,1,1,I,Int,pass

import "fmt"

type I interface {
	foo(a string, b int, v ...interface{})
	foo2(a string, b int, v ...interface{})
}

type Int int

func (n Int) foo(a string, b int, v ...interface{}) {
	fmt.Printf("b is %d\n", b)
	fmt.Printf(a, v...)
}

func main() {
	var three Int = 3
	var i I = three
	i.foo("Hello %s %s", 5, "cruel", "world")
}

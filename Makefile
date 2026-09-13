benchmark: benchmark.txt benchmark.txt~
	benchstat $^

benchmark.txt~:
	go test -run='^$$' -count 10 -benchmem -bench . | tee $@

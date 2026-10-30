package inproc

func transactionBenchmarkStoreCases() []transactionBenchmarkCase {
	return append(transactionBenchmarkStoreMessageCases(), transactionBenchmarkStoreMaintenanceCases()...)
}

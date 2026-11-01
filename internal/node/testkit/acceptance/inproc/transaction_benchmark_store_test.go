package inproc

func transactionBenchmarkStoreCases() []transactionBenchmarkCase {
	cases := append(transactionBenchmarkStoreMessageCases(), transactionBenchmarkStoreMaintenanceCases()...)
	return append(cases, transactionBenchmarkStoreTrustCases()...)
}

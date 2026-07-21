// M4-15-04: the umbrella evidence map proving the four Build-Plan-required
// edge-case categories -- wrong encodings, malformed/ragged/bad-mapping
// rows, in-file duplicates, oversized files -- are auditable-as-done.
// Recon (story M4-15) found the four categories already ~90% covered by
// M4-03/M4-06/M4-11; this file is the doc-only anchor tying that coverage
// together, greppable next to the suite it indexes. No assertions live
// here -- every test cited below is a real, already-passing test in this
// package (each re-verified against the code when this map was authored,
// 2026-07-21); this file only maps names to locations and states the M4-06
// exclusion boundary explicitly (Decision [case-c-membership]).
//
// A - Wrong encodings (5)
//
//	TestDecode_UTF8BOMStripped                 decode_test.go:133
//	TestDecode_UTF16LEBOMDecodesSameAsUTF8Twin  decode_test.go:164
//	TestDecode_UTF16BEBOMDecodesSameAsUTF8Twin  decode_adversarial_test.go:96
//	TestDecode_Windows1252NonUTF8Decodes        decode_test.go:205
//	TestFixtures_BadEncodingRejected            fixtures_edge_test.go:154
//	  (rewritten by M4-15-01: now asserts Decode-level rejection of
//	  edge_bad_encoding.csv, not the pre-guardrail svc.Import ErrValidation
//	  path -- Decision [bad-encoding-test-migration])
//
// B - Malformed rows / ragged / bad mapping / unparseable cells / column
// shape (10)
//
//	TestDecode_RaggedRowNoError                                             decode_test.go:238
//	TestServiceImport_BlankInvoiceNumberRowQuarantinedUngroupableScalarRow  service_test.go:668
//	TestServiceImport_NonNumericTotalQuarantinesViaCreateErrorOthersCommit  service_test.go:1102
//	TestServiceImport_UnparseableIssueDateQuarantines                       service_adversarial_test.go:135
//	TestServiceImport_MappingReferencesAbsentHeaderValidationBeforeAnyWrite service_test.go:755
//	TestServiceImport_MappingUnknownKeyValidationBeforeAnyWrite             service_test.go:798
//	TestFixtures_MissingColumnsRejectedPreWrite                             fixtures_edge_test.go:100
//	TestCreateHandler_BadMapping400                                         handlers_test.go:381
//	TestPreviewHandler_RaggedRowUnpadded                                    handlers_preview_test.go:313
//	TestPreviewHandler_VeryWideHeader                                       handlers_preview_adversarial_test.go:297
//
// C - In-file duplicates: same invoice_number repeated within ONE upload,
// grouping + header-conflict path (6). Genuine in-file only: multiple rows
// sharing an invoice_number in a single upload, grouped or quarantined by
// the in-file headerConflictField / "rows disagree on X" path -- no
// pre-seeded stored invoice, no create-time race. Re-verified against the
// code for this file: none of the six call seedInvoice and none race
// separate svc.Import calls at Create time -- each makes exactly one
// svc.Import call over one in-memory batch of rows.
//
//	TestFixtures_InFileDupesQuarantined                                                fixtures_edge_test.go:113
//	TestServiceImport_GroupDisagreeingOnTotalQuarantinedOthersCommit                   service_test.go:346
//	TestServiceImport_HeaderFieldConflictRowErrorCitesExactSheetRows                   service_test.go:459
//	TestServiceImport_LineFieldsVaryAcrossGroupRowsNoConflictCommitsWithDistinctLines  service_adversarial_test.go:327
//	TestServiceImport_HeaderSubtotalNormalizedBeforeConflictCompareNoSpuriousConflict  service_adversarial_test.go:369
//	TestServiceImport_HeaderIssueDateTrimmedBeforeConflictCompareNoSpuriousConflict    service_adversarial_test.go:414
//
// Excluded (M4-06 against-stored / create-time-race dedup -- Out of Scope,
// NOT part of M4-15's count; listed so the boundary is visible, not
// silently dropped):
//
//	TestServiceImport_NonContiguousMultiRowDuplicateGroupCitesAllRowsSorted       service_dup_adversarial_test.go:356
//	  (pre-seeds an existing invoice via seedInvoice, then asserts
//	  RuleKey == ruleKeyDuplicateInvoiceNumber -- against-stored, M4-06-01)
//	TestServiceImport_ConcurrentDuplicateAtCreateTimeQuarantinesLoser             service_test.go:900
//	  (separate single-row imports racing at Create time via goroutines;
//	  nothing duplicated within one file -- create-time backstop, M4-06-01)
//	TestServiceImport_EntityScopedDedupSameNumberUnderDifferentEntityNotDuplicate service_test.go:1164
//	  (pre-seeds INV-DUP under entity B, imports one row under entity A --
//	  against-stored entity-scoping, M4-06)
//
// D - Oversized-file rejection (6)
//
//	TestCreateHandler_OversizedBody413                       handlers_test.go:354
//	TestFixtures_OversizedRejected413                        fixtures_edge_test.go:277
//	TestPreviewHandler_OversizedBodyWithIdentity413           handlers_preview_test.go:336
//	TestPreviewHandler_NoIdentityOversizedBody401              handlers_preview_test.go:104
//	TestCreateHandler_NoIdentityOversizedBody401NotTooLarge    handlers_adversarial_test.go:55
//	TestDecode_XLSXUnzipSizeLimitEnforced (zip-bomb / unzip cap) decode_adversarial_test.go:219
//
// M4-15 net-new additions (3 subtasks, on top of the 27 above):
//
//	M4-15-01  the encoding guardrail itself (decode.go, the post-sniff-switch
//	          control-byte gate) plus its decode-level tests:
//	            TestDecode_CleanHeaderGarbageDataRejected        decode_adversarial_test.go:282
//	            TestDecode_NULByteRejected                       decode_adversarial_test.go:297
//	            TestDecode_UTF16LENoBOMRejectedViaGate            decode_adversarial_test.go:314
//	            TestDecode_TabDelimitedCRLFNotRejected            decode_adversarial_test.go:331
//	              (whitelist regression guard)
//	          plus 6 adversarial C0-boundary/per-branch cases:
//	            TestDecode_C0BoundaryPrecise                      decode_adversarial_test.go:359
//	            TestDecode_DELByteNotRejected                     decode_adversarial_test.go:436
//	            TestDecode_GateFiresOnUTF8BOMBranch               decode_adversarial_test.go:454
//	            TestDecode_GateFiresOnWindows1252FallbackBranch   decode_adversarial_test.go:474
//	            TestDecode_EmptyInputNotRejectedByGate            decode_adversarial_test.go:496
//	            TestDecode_AllWhitelistedControlsAccepted         decode_adversarial_test.go:520
//	          and the rewritten TestFixtures_BadEncodingRejected (see Case A).
//	M4-15-02  TestPreviewHandler_MalformedCSV400              handlers_preview_test.go:422
//	          TestPreviewHandler_BareQuoteInUnquotedField400  handlers_preview_test.go:451
//	M4-15-03  TestServiceImport_ColumnCountAnomaliesDegradeGracefully  service_column_anomaly_test.go:48
//	          TestServiceImport_DrasticallyShortRowNoPanic            service_column_anomaly_test.go:124
//
// Against-stored duplicate detection (entity + invoice number vs. an
// already-persisted invoice) is explicitly M4-06's row, not this one --
// see the Excluded list under Case C above.
package importer

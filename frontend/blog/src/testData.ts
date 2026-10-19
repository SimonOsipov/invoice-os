import type { BlogData } from './data'

export const TEST_DATA: BlogData = {
  categories: [
    { id: 'c1', name: 'Regulation', headline: 'Rules that move', intro: 'What the tax authority changed.', icon: 'landmark', tone: 0, post_count: 2 },
    { id: 'c2', name: 'Product', headline: 'What shipped', intro: 'New in ASComply.', icon: 'globe', tone: 1, post_count: 2 },
  ],
  posts: [
    {
      id: 'sage-rules', category_id: 'c1', publish_date: '2026-10-14', title: 'New sage rules for invoices',
      summary: 'A short summary of the rules.', author_name: 'Amara Okafor', featured: true, cover: null,
      body: [
        { type: 'p', text: 'Intro paragraph.' },
        { type: 'h', text: 'What changed' },
        { type: 'ul', items: ['First item', 'Second item'] },
        { type: 'h', text: 'What to do' },
        { type: 'note', kind: 'deadline', title: 'Cut-off', text: 'File by month end.' },
      ],
    },
    {
      id: 'tin-checks', category_id: 'c1', publish_date: '2026-09-30', title: 'TIN checks explained',
      summary: 'Why a TIN fails.', author_name: 'Tunde Bakare', featured: false, cover: null,
      body: [{ type: 'p', text: 'A TIN has a checksum.' }, { type: 'quote', text: 'Check twice.', who: 'An accountant' }],
    },
    {
      id: 'csv-import', category_id: 'c2', publish_date: '2026-09-12', title: 'Importing a CSV',
      summary: 'Bring your invoices in.', author_name: 'Zainab Yusuf', featured: false, cover: null,
      body: [{ type: 'p', text: 'Upload a file.' }, { type: 'demo', demo: 'import' }],
    },
    {
      id: 'approvals', category_id: 'c2', publish_date: '2026-08-01', title: 'Approvals at a glance',
      summary: 'Approve in one click.', author_name: 'Chidi Eze', featured: false, cover: null,
      body: [{ type: 'p', text: 'Pending invoices wait.' }, { type: 'note', kind: 'product', title: 'Now live', text: 'Available to all.' }],
    },
  ],
}

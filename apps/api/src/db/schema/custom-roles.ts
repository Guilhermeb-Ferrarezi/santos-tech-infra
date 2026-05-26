import { jsonb, pgTable, text, timestamp, uuid } from 'drizzle-orm/pg-core'

export type ScopeAction = 'read' | 'write' | 'delete'
export type Permissions = Partial<Record<string, ScopeAction[]>>

export const customRoles = pgTable('custom_roles', {
  id: uuid('id').primaryKey().defaultRandom(),
  name: text('name').notNull().unique(),
  description: text('description'),
  permissions: jsonb('permissions').notNull().default({}).$type<Permissions>(),
  createdAt: timestamp('created_at').notNull().defaultNow(),
})

export type CustomRole = typeof customRoles.$inferSelect
export type NewCustomRole = typeof customRoles.$inferInsert

import { pgTable, text, timestamp, uuid, index } from 'drizzle-orm/pg-core'
import { users } from './users'

export const passwordResets = pgTable('password_resets', {
  id: uuid('id').primaryKey().defaultRandom(),
  userId: uuid('user_id').notNull().references(() => users.id, { onDelete: 'cascade' }),
  tokenHash: text('token_hash').notNull().unique(),
  expiresAt: timestamp('expires_at').notNull(),
  createdAt: timestamp('created_at').notNull().defaultNow(),
}, (table) => [
  index('password_resets_user_id_idx').on(table.userId),
])

export type PasswordReset = typeof passwordResets.$inferSelect

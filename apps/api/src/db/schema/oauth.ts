import { integer, pgTable, text, timestamp, uuid, unique } from 'drizzle-orm/pg-core'
import { users } from './users'

export const oauthAccounts = pgTable('oauth_accounts', {
  id: uuid('id').primaryKey().defaultRandom(),
  userId: integer('user_id').notNull().references(() => users.id, { onDelete: 'cascade' }),
  provider: text('provider').notNull(),
  providerId: text('provider_id').notNull(),
  createdAt: timestamp('created_at').notNull().defaultNow(),
}, (t) => [
  unique().on(t.provider, t.providerId),
])

export type OauthAccount = typeof oauthAccounts.$inferSelect

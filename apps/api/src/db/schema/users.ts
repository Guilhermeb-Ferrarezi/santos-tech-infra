import { pgTable, serial, smallint, text, timestamp, uuid } from 'drizzle-orm/pg-core'
import { customRoles } from './custom-roles'

export const users = pgTable('users', {
  id: serial('id').primaryKey(),
  email: text('email').notNull().unique(),
  username: text('username').unique(),
  name: text('name').notNull(),
  passwordHash: text('password_hash'),
  avatarUrl: text('avatar_url'),
  role: smallint('role').notNull().default(1),
  customRoleId: uuid('custom_role_id').references(() => customRoles.id, { onDelete: 'set null' }),
  suspendedAt: timestamp('suspended_at'),
  createdAt: timestamp('created_at').notNull().defaultNow(),
})

export type User = typeof users.$inferSelect
export type NewUser = typeof users.$inferInsert

// 1 = Student, 2 = Teacher, 3 = Admin, 4 = Custom
export const Role = { Student: 1, Teacher: 2, Admin: 3, Custom: 4 } as const
export type RoleValue = typeof Role[keyof typeof Role]

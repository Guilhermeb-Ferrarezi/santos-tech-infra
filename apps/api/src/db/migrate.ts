import { drizzle } from 'drizzle-orm/node-postgres'
import { migrate } from 'drizzle-orm/node-postgres/migrator'
import { Pool } from 'pg'
import { env } from '@santos-tech/env'

const pool = new Pool({ connectionString: env.DATABASE_URL })
const db = drizzle(pool)

await migrate(db, { migrationsFolder: './drizzle' })
console.log('Migrations executadas com sucesso')
await pool.end()

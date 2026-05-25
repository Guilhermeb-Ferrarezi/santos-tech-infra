import type { FastifyInstance } from 'fastify'
import oauthPlugin from '@fastify/oauth2'
import { eq } from 'drizzle-orm'
import { db } from '@/db/client'
import { users, oauthAccounts, sessions } from '@/db/schema'
import { env } from '@santos-tech/env'
import { AppError } from '@/shared/errors/app-error'
import { generateTokens, hashRefreshToken, cookieOptions } from './auth.service'

export async function registerOAuth(app: FastifyInstance) {
  await app.register(oauthPlugin, {
    name: 'googleOAuth',
    credentials: {
      client: {
        id: env.GOOGLE_CLIENT_ID,
        secret: env.GOOGLE_CLIENT_SECRET,
      },
      auth: oauthPlugin.GOOGLE_CONFIGURATION,
    },
    startRedirectPath: '/auth/google',
    callbackUri: env.GOOGLE_CALLBACK_URL,
    scope: ['email', 'profile'],
  })

  app.get('/auth/google/callback', async (request, reply) => {
    if ((request.query as any).error) {
      throw new AppError(401, 'OAUTH_DENIED', 'Autenticação Google negada')
    }

    let token: { access_token: string }
    try {
      token = await (app as any).googleOAuth.getAccessTokenFromAuthorizationCodeFlow(request)
    } catch {
      throw new AppError(401, 'OAUTH_FAILED', 'Falha ao obter token do Google')
    }

    const profileRes = await fetch('https://www.googleapis.com/oauth2/v2/userinfo', {
      headers: { Authorization: `Bearer ${token.access_token}` },
    })
    const profile = (await profileRes.json()) as { id: string; email: string; name: string }

    const [user] = await db.select().from(users).where(eq(users.email, profile.email)).limit(1)
    if (!user) {
      throw new AppError(401, 'ACCOUNT_NOT_FOUND', 'Nenhuma conta encontrada com este email Google. Cadastre-se primeiro.')
    }

    const existing = await db
      .select()
      .from(oauthAccounts)
      .where(eq(oauthAccounts.providerId, profile.id))
      .limit(1)

    if (existing.length === 0) {
      await db.insert(oauthAccounts).values({
        userId: user.id,
        provider: 'google',
        providerId: profile.id,
      })
    }

    const { accessToken, refreshToken } = await generateTokens(user.id)
    const expiresAt = new Date()
    expiresAt.setDate(expiresAt.getDate() + 7)
    await db.insert(sessions).values({
      userId: user.id,
      refreshTokenHash: hashRefreshToken(refreshToken),
      expiresAt,
    })

    const opts = cookieOptions(env)
    reply
      .setCookie('access_token', accessToken, { ...opts, maxAge: 15 * 60 })
      .setCookie('refresh_token', refreshToken, { ...opts, maxAge: 7 * 24 * 60 * 60 })

    return reply.redirect(env.CORS_ORIGIN)
  })
}

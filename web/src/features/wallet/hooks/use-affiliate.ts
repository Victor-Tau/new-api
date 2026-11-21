import { useQuery } from '@tanstack/react-query'
/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import i18next from 'i18next'
import { useState, useCallback } from 'react'
import { toast } from 'sonner'

import { getSelf } from '@/lib/api'
import { handleServerError } from '@/lib/handle-server-error'
import {
  getServerMessage,
  requireServerSuccess,
} from '@/lib/server-error-message'
import { useAuthStore } from '@/stores/auth-store'

import {
  getAffiliateCode,
  getReferralPermissions,
  transferAffiliateQuota,
} from '../api'
import { generateAffiliateLink } from '../lib'

// ============================================================================
// Affiliate Hook
// ============================================================================

export function useAffiliate() {
  const userId = useAuthStore((state) => state.auth.user?.id)
  const [transferring, setTransferring] = useState(false)
  const permissions = useQuery({
    queryKey: ['referral-permissions', userId],
    queryFn: getReferralPermissions,
    enabled: userId !== undefined,
  })
  const invitationEnabled =
    permissions.isSuccess && permissions.data.invitation_enabled
  const code = useQuery({
    queryKey: ['referral-code', userId],
    enabled: invitationEnabled,
    queryFn: async () => requireServerSuccess(await getAffiliateCode()).data,
  })
  const affiliateLink =
    invitationEnabled && code.data ? generateAffiliateLink(code.data) : ''

  // Transfer affiliate quota to balance
  const transferQuota = useCallback(async (quota: number): Promise<boolean> => {
    try {
      setTransferring(true)
      const response = await transferAffiliateQuota({ quota })

      if (response.success) {
        toast.success(
          getServerMessage(response) || i18next.t('Transfer successful')
        )
        await getSelf()
        return true
      }

      handleServerError(response, i18next.t('Transfer failed'))
      return false
    } catch (_error) {
      handleServerError(_error, i18next.t('Transfer failed'))
      return false
    } finally {
      setTransferring(false)
    }
  }, [])

  return {
    affiliateLink,
    invitationEnabled,
    permissions: permissions.isSuccess ? permissions.data : undefined,
    permissionsError: permissions.isError,
    loading: invitationEnabled && code.isPending,
    transferring,
    transferQuota,
    userId,
  }
}

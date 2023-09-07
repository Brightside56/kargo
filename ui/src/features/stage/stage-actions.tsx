import { faPen, faRefresh, faTrash } from '@fortawesome/free-solid-svg-icons';
import { useMutation } from '@tanstack/react-query';
import { Button, Space } from 'antd';
import { generatePath, useNavigate, useParams } from 'react-router-dom';

import { paths } from '@ui/config/paths';
import {
  deleteStage,
  refreshStage
} from '@ui/gen/service/v1alpha1/service-KargoService_connectquery';
import { Stage } from '@ui/gen/v1alpha1/types_pb';

import { ButtonIcon } from '../common';
import { useConfirmModal } from '../common/confirm-modal/use-confirm-modal';
import { useModal } from '../common/modal/use-modal';

import { EditStageModal } from './edit-stage-modal';

export const StageActions = ({ stage }: { stage: Stage }) => {
  const { name: projectName, stageName } = useParams();
  const navigate = useNavigate();
  const confirm = useConfirmModal();
  const { mutate, isLoading: isLoadingDelete } = useMutation(deleteStage.useMutation());
  const { mutate: refresh, isLoading: isRefreshLoading } = useMutation(refreshStage.useMutation());

  const onClose = () => navigate(generatePath(paths.project, { name: projectName }));

  const onDelete = () => {
    confirm({
      onOk: () => {
        mutate({ name: stage.metadata?.name, project: projectName });
        onClose();
      },
      title: 'Are you sure you want to delete Stage?'
    });
  };

  const onRefresh = () => refresh({ name: stageName, project: projectName });

  const { show: showEditStageModal } = useModal((p) =>
    stageName && projectName ? (
      <EditStageModal {...p} stageName={stageName} projectName={projectName} />
    ) : null
  );

  return (
    <Space size={16}>
      <Button
        type='text'
        icon={<ButtonIcon icon={faPen} size='1x' />}
        onClick={() => showEditStageModal()}
      >
        Edit
      </Button>
      <Button
        type='default'
        icon={<ButtonIcon icon={faRefresh} size='1x' />}
        onClick={onRefresh}
        loading={isRefreshLoading || !!stage?.metadata?.annotations['kargo.akuity.io/refresh']}
      >
        Refresh
      </Button>
      <Button
        danger
        type='text'
        icon={<ButtonIcon icon={faTrash} size='1x' />}
        onClick={onDelete}
        loading={isLoadingDelete}
        size='small'
      >
        Delete
      </Button>
    </Space>
  );
};